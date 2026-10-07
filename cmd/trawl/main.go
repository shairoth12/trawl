// Command trawl analyzes a Go package call graph and reports external service
// calls reachable from a given entry point function.
//
// Usage:
//
//	trawl --pkg <package_pattern> --entry <function_name> [--config <yaml>] [--algo vta|rta|cha] [--deps auto|none]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	goversion "go/version"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/ssa"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/analysis"
	"github.com/shairoth12/trawl/internal/detector"
	"github.com/shairoth12/trawl/internal/walker"
)

// version, commit, and date are injected at build time via:
//
//	-ldflags "-X main.version=vX.Y.Z -X main.commit=abc1234 -X main.date=2006-01-02"
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// versionInfo returns the human-readable version string, including the Go
// version the binary was compiled with, the git commit, and the build date.
// Builds without ldflags, such as "go install module@version", report the
// module version that the go command records in the binary.
func versionInfo() string {
	v := version
	info, ok := debug.ReadBuildInfo()
	if v == "dev" && ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}
	return fmt.Sprintf("trawl %s (commit %s, built %s with %s)",
		v, commit, date, runtime.Version())
}

// toolchainWarning returns a non-empty warning string when the host toolchain
// is a newer Go release (major.minor) than the one trawl was built with.
// hostGoVersion should be the bare version string returned by "go env
// GOVERSION" (e.g. "go1.26.0"). Returns an empty string when the host is the
// same release or older, or when either version cannot be parsed (best-effort
// check, no hard failure).
//
// trawl type-checks the host's standard library source with the go/types
// built into the binary. A newer standard library may use language features
// that go/types does not know yet, which causes cryptic load errors. An older
// host is fine, and patch releases never change the language.
func toolchainWarning(hostGoVersion string) string {
	built := runtime.Version()
	host, builtLang := goversion.Lang(hostGoVersion), goversion.Lang(built)
	if host == "" || builtLang == "" || goversion.Compare(host, builtLang) <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"warning: trawl was built with %s but host toolchain is %s\n"+
			"         rebuild trawl with %s: go install github.com/shairoth12/trawl/cmd/trawl@latest",
		built, hostGoVersion, hostGoVersion,
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
		_, _ = fmt.Fprintln(w, "Usage: trawl --pkg <pattern> --entry <name> [--config <yaml>] [--algo vta|rta|cha] [--scope <patterns>] [--deps auto|none]")
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
	depsStr := fs.String("deps", string(analysis.DependencyAuto), "Dependency bodies: auto (build SSA for same-module packages and for dependency packages implementing interfaces the analyzed code invokes) or none")
	dedupFlag := fs.Bool("dedup", false, "Deduplicate results by (service_type, import_path, function), keeping shortest call chain")
	statsFlag := fs.Bool("stats", false, "Include analysis statistics in JSON output (packages loaded, call graph size, DFS counters, phase durations)")
	timeoutStr := fs.String("timeout", "10m", "Maximum duration for the analysis (e.g. 30s, 5m, 1h); 0 means no timeout")
	logLevel := fs.String("log-level", "info", "Log verbosity: off, error, warn, info, or debug")
	logFile := fs.String("log-file", "", "Write logs to this file instead of stderr")
	logFormat := fs.String("log-format", "text", "Log format: text or json")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *showVersion {
		_, err := fmt.Fprintln(stdout, versionInfo())
		return err
	}

	log, logCleanup, err := buildLogger(*logLevel, *logFormat, *logFile)
	if err != nil {
		return err
	}
	defer logCleanup()

	if warn := toolchainWarning(activeGoVersion()); warn != "" {
		log.Warn(warn)
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

	cfg, err := loadConfig(*configPath)
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
		for s := range strings.SplitSeq(*scope, ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopePatterns = append(scopePatterns, s)
			}
		}
	}

	det := detector.New(cfg.Indicators)

	log.Info("loading_packages", "pkg", *pkg, "algo", *algoStr, "deps", *depsStr)
	t0 := time.Now()
	loadResult, err := analysis.Load(ctx, analysis.Options{
		Dir: dir, Pattern: *pkg, Algo: algo, Scope: scopePatterns,
		DependencyPolicy: analysis.DependencyPolicy(*depsStr),
		IsIndicator:      func(p string) bool { _, ok := det.Detect(p); return ok },
	})
	if err != nil {
		return fmt.Errorf("loading package %q: %w", *pkg, err)
	}
	log.Info("packages_loaded", "pkg", *pkg, "elapsed", time.Since(t0).String())
	log.Info("dependency_bodies", "count", len(loadResult.DependencyPkgs))
	log.Debug("dependency_bodies_list", "pkgs", loadResult.DependencyPkgs)
	if n := loadResult.DependencyPkgsSkipped; n > 0 {
		log.Warn("dependency_bodies_truncated", "skipped", n, "limit", analysis.DefaultMaxDependencyPkgs)
	}

	log.Info("resolving_entry", "entry", *entry)
	fn, err := analysis.Resolve(loadResult, *entry)
	if err != nil {
		return fmt.Errorf("resolving entry point %q: %w", *entry, err)
	}
	log.Info("entry_resolved", "entry", *entry, "fn", fn.String())

	graph := loadResult.Graph
	if algo == analysis.AlgoRTA {
		rtaResult := rta.Analyze([]*ssa.Function{fn}, true)
		graph = rtaResult.CallGraph
	}
	// loadDuration covers package load + SSA build + call graph construction for
	// all algorithms. For VTA/CHA the call graph is built inside analysis.Load;
	// for RTA it is built by rta.Analyze above, so the timer stops here after
	// both phases complete.
	loadDuration := time.Since(t0)

	w := walker.New(graph, det, walker.Options{Module: loadResult.Module, DependencyPkgs: loadResult.DependencyPkgs, Stdlib: loadResult.Stdlib, Fset: loadResult.Prog.Fset, Log: log})
	log.Info("walking_graph", "entry", fn.String())
	t1 := time.Now()
	calls, walkStats, err := w.Walk(fn)
	if err != nil {
		return fmt.Errorf("walking call graph: %w", err)
	}
	walkDuration := time.Since(t1)
	log.Info("walk_complete", "calls", len(calls), "elapsed", walkDuration.String())

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

	out := trawl.Result{
		EntryPoint:    fn.String(),
		Package:       loadResult.SSAPkg.Pkg.Path(),
		ExternalCalls: calls,
	}
	if *dedupFlag {
		out.Deduplicated = true
	}

	if *statsFlag {
		out.Stats = &trawl.AnalysisStats{
			PackagesLoaded:     loadResult.PackagesLoaded,
			PackagesAnalyzed:   loadResult.PackagesAnalyzed,
			DependencyPackages: len(loadResult.DependencyPkgs),
			CallGraphNodes:     len(graph.Nodes),
			CallGraphEdges:     countGraphEdges(graph),
			NodesVisited:       walkStats.NodesVisited,
			EdgesExamined:      walkStats.EdgesExamined,
			UnresolvedInvokes:  walkStats.UnresolvedInvokes,
			LoadDurationMs:     loadDuration.Milliseconds(),
			WalkDurationMs:     walkDuration.Milliseconds(),
		}
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encoding output: %w", err)
	}
	return nil
}

// buildLogger constructs a *slog.Logger that writes to dst (or os.Stderr when
// dst is empty). Level "off" produces a discard logger. The returned cleanup
// func closes the log file when dst is non-empty; callers must defer it.
func buildLogger(level, format, dst string) (*slog.Logger, func(), error) {
	if strings.ToLower(level) == "off" {
		return slog.New(slog.NewTextHandler(io.Discard, nil)), func() {}, nil
	}

	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, nil, fmt.Errorf("invalid --log-level %q: must be off, error, warn, info, or debug", level)
	}

	switch strings.ToLower(format) {
	case "json", "text", "":
		// valid
	default:
		return nil, nil, fmt.Errorf("invalid --log-format %q: must be text or json", format)
	}

	out := io.Writer(os.Stderr)
	cleanup := func() {}
	if dst != "" {
		f, err := os.Create(dst)
		if err != nil {
			return nil, nil, fmt.Errorf("opening log file %s: %w", dst, err)
		}
		out = f
		cleanup = func() { _ = f.Close() }
	}

	opts := &slog.HandlerOptions{Level: lvl, AddSource: lvl == slog.LevelDebug}
	var h slog.Handler
	if strings.ToLower(format) == "json" {
		h = slog.NewJSONHandler(out, opts)
	} else {
		h = slog.NewTextHandler(out, opts)
	}
	return slog.New(h), cleanup, nil
}

// countGraphEdges returns the total number of outgoing call edges across all
// nodes in graph. Returns 0 for a nil graph.
func countGraphEdges(graph *callgraph.Graph) int {
	if graph == nil {
		return 0
	}
	total := 0
	for _, node := range graph.Nodes {
		total += len(node.Out)
	}
	return total
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
	result := make([]trawl.ExternalCall, 0, len(calls)) // non-nil: external_calls is never null
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
