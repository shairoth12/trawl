// Command trawl analyzes a Go package call graph and reports external service
// calls reachable from a given entry point function.
//
// Usage:
//
//	trawl --pkg <package_pattern> --entry <function_name> [--config <yaml>] [--algo vta|rta]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/ssa"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/analysis"
	"github.com/shairoth12/trawl/internal/detector"
	"github.com/shairoth12/trawl/internal/walker"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "trawl: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("trawl", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		w := fs.Output()
		_, _ = fmt.Fprintln(w, "Usage: trawl --pkg <pattern> --entry <name> [--config <yaml>] [--algo vta|rta|cha] [--scope <patterns>]")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Flags:")
		fs.PrintDefaults()
	}

	pkg := fs.String("pkg", ".", "Go package pattern to analyze")
	entry := fs.String("entry", "", "Entry point function name (required)")
	configPath := fs.String("config", "", "Path to YAML config file for custom indicators")
	algoStr := fs.String("algo", string(analysis.AlgoVTA), "Call graph algorithm: vta (default), rta, or cha")
	scope := fs.String("scope", "", "Extra package patterns for type visibility (comma-separated)")
	dedupFlag := fs.Bool("dedup", false, "Deduplicate results by (service_type, import_path, function), keeping shortest call chain")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *entry == "" {
		fs.Usage()
		return fmt.Errorf("--entry is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := trawl.LoadConfig(ctx, *configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	algo := analysis.Algo(*algoStr)
	var scopePatterns []string
	if *scope != "" {
		for _, s := range strings.Split(*scope, ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopePatterns = append(scopePatterns, s)
			}
		}
	}
	loadResult, err := analysis.Load(ctx, dir, *pkg, algo, scopePatterns...)
	if err != nil {
		return fmt.Errorf("loading package %q: %w", *pkg, err)
	}

	fn, err := analysis.Resolve(loadResult, *entry)
	if err != nil {
		return fmt.Errorf("resolving entry point %q: %w", *entry, err)
	}

	graph := loadResult.Graph
	if algo == analysis.AlgoRTA {
		rtaResult := rta.Analyze([]*ssa.Function{fn}, true)
		graph = rtaResult.CallGraph
	}

	det := detector.New(cfg.Indicators)
	w := walker.New(graph, det, loadResult.Module, loadResult.Prog.Fset)
	calls, err := w.Walk(fn)
	if err != nil {
		return fmt.Errorf("walking call graph: %w", err)
	}

	if *dedupFlag {
		calls = deduplicateCalls(calls)
	}

	for i := range calls {
		calls[i].ShortFunction = trawl.ShortenName(calls[i].Function)
		calls[i].ShortCallChain = make([]string, len(calls[i].CallChain))
		for j, name := range calls[i].CallChain {
			calls[i].ShortCallChain[j] = trawl.ShortenName(name)
		}
	}

	out := trawl.NewResult(fn.String(), loadResult.SSAPkg.Pkg.Path())
	out.ExternalCalls = calls
	if *dedupFlag {
		out.Deduplicated = true
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encoding output: %w", err)
	}
	return nil
}

type dedupKey struct {
	serviceType trawl.ServiceType
	importPath  string
	function    string
}

// deduplicateCalls removes duplicate external calls keyed by
// (ServiceType, ImportPath, Function), keeping the entry with the shortest
// CallChain among duplicates.
func deduplicateCalls(calls []trawl.ExternalCall) []trawl.ExternalCall {
	if calls == nil {
		return nil
	}
	seen := make(map[dedupKey]int, len(calls))
	var result []trawl.ExternalCall
	for _, ec := range calls {
		key := dedupKey{
			serviceType: ec.ServiceType,
			importPath:  ec.ImportPath,
			function:    ec.Function,
		}
		if idx, exists := seen[key]; exists {
			if len(ec.CallChain) < len(result[idx].CallChain) {
				result[idx] = ec
			}
			continue
		}
		seen[key] = len(result)
		result = append(result, ec)
	}
	return result
}
