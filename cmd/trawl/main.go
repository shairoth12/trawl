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
		_, _ = fmt.Fprintln(w, "Usage: trawl --pkg <pattern> --entry <name> [--config <yaml>] [--algo vta|rta]")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Flags:")
		fs.PrintDefaults()
	}

	pkg := fs.String("pkg", ".", "Go package pattern to analyze")
	entry := fs.String("entry", "", "Entry point function name (required)")
	configPath := fs.String("config", "", "Path to YAML config file for custom indicators")
	algoStr := fs.String("algo", string(analysis.AlgoVTA), "Call graph algorithm: vta (default) or rta")

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
	loadResult, err := analysis.Load(ctx, dir, *pkg, algo)
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

	out := trawl.NewResult(fn.String(), loadResult.SSAPkg.Pkg.Path())
	out.ExternalCalls = calls

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encoding output: %w", err)
	}
	return nil
}
