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
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/ssa"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/analysis"
	"github.com/shairoth12/trawl/internal/detector"
	"github.com/shairoth12/trawl/internal/walker"
)

// version is the binary version, injected at build time via:
//
//	-ldflags "-X main.version=vX.Y.Z"
var version = "dev"

// versionInfo returns the human-readable version string, including the Go
// version the binary was compiled with.
func versionInfo() string {
	return fmt.Sprintf("trawl %s (built with %s)", version, runtime.Version())
}

// toolchainWarning returns a non-empty warning string when the binary's
// compile-time Go version differs from the active host toolchain version.
// hostGoVersion should be the bare version string returned by "go env GOVERSION"
// (e.g. "go1.25.0"). Returns an empty string when the versions match or when
// hostGoVersion is empty (best-effort check, no hard failure).
//
// trawl shells out to the host "go" command via go/packages; a mismatch can
// cause cryptic load errors, so surfacing it early helps users self-diagnose.
func toolchainWarning(hostGoVersion string) string {
	if hostGoVersion == "" {
		return ""
	}
	built := runtime.Version()
	if built == hostGoVersion {
		return ""
	}
	return fmt.Sprintf(
		"warning: trawl was built with %s but host toolchain is %s\n"+
			"         consider: go install github.com/shairoth12/trawl/cmd/trawl@latest",
		built, hostGoVersion,
	)
}

// activeGoVersion runs "go env GOVERSION" and returns the trimmed output.
// Returns an empty string on any error so the caller can treat this as a
// best-effort probe.
func activeGoVersion() string {
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "trawl: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("trawl", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		w := fs.Output()
		_, _ = fmt.Fprintln(w, "Usage: trawl --pkg <pattern> --entry <name> [--config <yaml>] [--algo vta|rta|cha] [--scope <patterns>]")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Flags:")
		fs.PrintDefaults()
	}

	showVersion := fs.Bool("version", false, "Print version and exit")
	pkg := fs.String("pkg", ".", "Go package pattern to analyze")
	entry := fs.String("entry", "", "Entry point function name (required)")
	configPath := fs.String("config", "", "Path to YAML config file for custom indicators")
	algoStr := fs.String("algo", string(analysis.AlgoVTA), "Call graph algorithm: vta (default), rta, or cha")
	scope := fs.String("scope", "", "Extra package patterns for type visibility (comma-separated)")
	dedupFlag := fs.Bool("dedup", false, "Deduplicate results by (service_type, import_path, function), keeping shortest call chain")
	timeoutStr := fs.String("timeout", "10m", "Maximum duration for the analysis (e.g. 30s, 5m, 1h); 0 means no timeout")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *showVersion {
		fmt.Fprintln(stdout, versionInfo())
		return nil
	}

	if warn := toolchainWarning(activeGoVersion()); warn != "" {
		fmt.Fprintln(os.Stderr, warn)
	}

	if *entry == "" {
		fs.Usage()
		return fmt.Errorf("--entry is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *timeoutStr != "" {
		d, err := time.ParseDuration(*timeoutStr)
		if err != nil {
			return fmt.Errorf("invalid --timeout %q: %w", *timeoutStr, err)
		}
		if d > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
	}

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

	// Strip the working-directory prefix from file paths so output contains
	// relative paths rather than absolute filesystem paths.
	for i := range calls {
		if calls[i].File != "" {
			rel, relErr := filepath.Rel(dir, calls[i].File)
			calls[i].File = rel
			if relErr != nil || strings.HasPrefix(rel, "..") {
				calls[i].File = "" // path cannot be made relative to cwd; omit
			}
		}
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

	enc := json.NewEncoder(stdout)
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
