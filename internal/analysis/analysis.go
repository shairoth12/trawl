// Package analysis loads a Go package, builds SSA form — including function
// bodies for same-module packages and for dependency packages that implement
// interfaces the analyzed code invokes — and constructs a call graph using
// VTA, RTA, or CHA.
package analysis

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// LoadResult holds the outcome of a successful package load and SSA build.
// All pointer fields are read-only after Load returns; callers must not mutate
// the underlying SSA program, call graph, or package through these pointers.
type LoadResult struct {
	// Prog is the SSA program built from all transitively-loaded packages.
	Prog *ssa.Program

	// Graph is the call graph produced by the VTA pipeline. It is nil when
	// algo is AlgoRTA; the caller must call rta.Analyze after resolving the
	// entry point and assign the resulting graph.
	Graph *callgraph.Graph

	// SSAPkg is the SSA representation of the directly-analyzed package
	// (not transitive dependencies).
	SSAPkg *ssa.Package

	// Module is the module path extracted from go.mod (e.g. "github.com/foo/bar").
	// It is empty for GOPATH workspaces that have no go.mod.
	Module string

	// PackagesLoaded is the total number of packages transitively loaded into
	// the build, including dependencies. Useful for diagnosing slow analysis.
	PackagesLoaded int

	// DependencyPkgs lists the non-initial packages whose function bodies were
	// built: same-module packages (sorted by path), then cross-module packages
	// ordered by selection round and path.
	DependencyPkgs []string

	// DependencyPkgsSkipped counts cross-module packages dropped by
	// Options.MaxDependencyPkgs.
	DependencyPkgsSkipped int

	// PackagesAnalyzed is the number of packages with built bodies: the
	// initial packages plus len(DependencyPkgs).
	PackagesAnalyzed int
}

// DepPolicy selects which non-initial packages receive SSA function bodies.
type DepPolicy string

const (
	// DepAuto builds bodies for same-module packages and for dependency
	// packages that implement interfaces the analyzed code invokes.
	DepAuto DepPolicy = "auto"
	// DepNone builds bodies only for the initial packages (legacy behavior).
	DepNone DepPolicy = "none"
)

// DefaultMaxDependencyPkgs bounds the number of cross-module packages that
// receive bodies under DepAuto when Options.MaxDependencyPkgs is zero.
const DefaultMaxDependencyPkgs = 200

// Options configures Load.
type Options struct {
	Dir     string   // working directory for go/packages, typically the module root
	Pattern string   // package pattern to analyze, e.g. "." or "./cmd/server"
	Algo    Algo     // call graph algorithm; "" defaults to AlgoVTA
	Scope   []string // extra package patterns loaded as initial packages

	Deps              DepPolicy // "" defaults to DepAuto
	MaxDependencyPkgs int       // cap on cross-module packages with bodies; 0 → DefaultMaxDependencyPkgs

	// IsIndicator reports whether an import path matches a detector
	// indicator. Matching packages never receive bodies: the walker records
	// and stops at them anyway. nil disables the exclusion.
	IsIndicator func(importPath string) bool
}

// Algo identifies the call graph construction algorithm.
type Algo string

const (
	// AlgoVTA uses Variable Type Analysis (default).
	AlgoVTA Algo = "vta"
	// AlgoRTA uses Rapid Type Analysis (requires an entry point).
	AlgoRTA Algo = "rta"
	// AlgoCHA uses Class Hierarchy Analysis. CHA resolves interface dispatch
	// purely by structural type matching, without tracking value flow. Use CHA
	// when analyzing code that uses reflection-based DI frameworks (dig, fx)
	// where VTA cannot trace concrete-to-interface assignments.
	AlgoCHA Algo = "cha"
)

// ErrPackageLoad is returned when one or more packages fail to load.
var ErrPackageLoad = errors.New("package load errors")

// Load loads the package at opts.Pattern from opts.Dir, builds SSA form, and
// constructs a call graph using opts.Algo.
//
// opts.Scope adds initial packages for type visibility. opts.Deps controls
// which dependency packages also receive function bodies (see DepPolicy);
// opts.MaxDependencyPkgs caps the cross-module ones and opts.IsIndicator
// excludes packages the detector already classifies.
//
// For AlgoRTA, LoadResult.Graph is nil; the caller must resolve an entry point
// and call rta.Analyze to produce the graph.
//
// ctx is propagated into package loading and checked before each expensive
// phase.
func Load(ctx context.Context, opts Options) (*LoadResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("ctx must not be nil")
	}
	switch opts.Deps {
	case "":
		opts.Deps = DepAuto
	case DepAuto, DepNone:
	default:
		return nil, fmt.Errorf("unknown dependency policy %q: supported values are %q and %q", opts.Deps, DepAuto, DepNone)
	}

	allPatterns := make([]string, 0, 1+len(opts.Scope))
	allPatterns = append(allPatterns, opts.Pattern)
	for _, sp := range opts.Scope {
		sp = strings.TrimSpace(sp)
		if sp != "" && sp != opts.Pattern {
			allPatterns = append(allPatterns, sp)
		}
	}
	pkgs, err := loadPackages(ctx, opts.Dir, allPatterns)
	if err != nil {
		return nil, err
	}

	// Check cancellation before the expensive SSA build.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var pkgCount int
	packages.Visit(pkgs, func(*packages.Package) bool { pkgCount++; return true }, nil)

	modulePath := modulePathOf(pkgs, opts.Dir, opts.Pattern)

	var deps []*packages.Package
	var skipped int
	if opts.Deps == DepAuto {
		modulePkgs, external := selectDependencyPkgs(pkgs, modulePath, opts.IsIndicator)
		limit := opts.MaxDependencyPkgs
		if limit <= 0 {
			limit = DefaultMaxDependencyPkgs
		}
		if len(external) > limit {
			skipped = len(external) - limit
			external = external[:limit]
		}
		deps = append(modulePkgs, external...)
	}
	withBodies := make(map[*packages.Package]bool, len(deps))
	depPaths := make([]string, 0, len(deps))
	for _, p := range deps {
		withBodies[p] = true
		depPaths = append(depPaths, p.PkgPath)
	}

	prog, ssaPkgs := createProgram(pkgs, withBodies, ssa.InstantiateGenerics)
	if err := buildProgram(prog); err != nil {
		return nil, err
	}

	ssaPkg, err := resolveSSAPkg(ssaPkgs, pkgs, opts.Dir, opts.Pattern, opts.Scope)
	if err != nil {
		return nil, err
	}

	result := &LoadResult{
		Prog:                  prog,
		SSAPkg:                ssaPkg,
		Module:                modulePath,
		PackagesLoaded:        pkgCount,
		DependencyPkgs:        depPaths,
		DependencyPkgsSkipped: skipped,
		PackagesAnalyzed:      len(ssaPkgs) + len(depPaths),
	}

	return buildGraph(ctx, result, opts.Algo, opts.Pattern)
}

// createProgram mirrors ssautil.Packages but also attaches syntax — and so
// function bodies — to the dependency packages in withBodies. Every package is
// created exactly once, in dependency order (packages.Visit post-order).
func createProgram(initial []*packages.Package, withBodies map[*packages.Package]bool, mode ssa.BuilderMode) (*ssa.Program, []*ssa.Package) {
	var fset *token.FileSet
	if len(initial) > 0 {
		fset = initial[0].Fset
	}
	prog := ssa.NewProgram(fset, mode)
	isInitial := make(map[*packages.Package]bool, len(initial))
	for _, p := range initial {
		isInitial[p] = true
	}
	created := make(map[*packages.Package]*ssa.Package)
	packages.Visit(initial, nil, func(p *packages.Package) {
		if p.Types == nil || p.IllTyped {
			return
		}
		var files []*ast.File
		var info *types.Info
		if isInitial[p] || withBodies[p] {
			files, info = p.Syntax, p.TypesInfo
		}
		created[p] = prog.CreatePackage(p.Types, files, info, true)
	})
	ssaPkgs := make([]*ssa.Package, len(initial))
	for i, p := range initial {
		ssaPkgs[i] = created[p] // nil for ill-typed packages, as with ssautil.Packages
	}
	return prog, ssaPkgs
}

// buildProgram builds every created package. Program.Build cannot be used:
// it runs each package in a goroutine whose panic would escape the caller.
// Package.Build is idempotent and safe to call concurrently, so packages are
// built here in bounded goroutines, each converting a panic into an error.
func buildProgram(prog *ssa.Program) error {
	pkgs := prog.AllPackages()
	errs := make([]error, len(pkgs))
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i, p := range pkgs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := recoverBuild(p.Build); err != nil {
				errs[i] = fmt.Errorf("package %s: %w", p.Pkg.Path(), err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// recoverBuild runs build, converting a panic into an error that names the
// escape hatch.
func recoverBuild(build func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("building SSA: %v (retry with --deps none to exclude dependency bodies)", r)
		}
	}()
	build()
	return nil
}

// loadPackages runs go/packages for patterns from dir with the full mode
// trawl needs (syntax and type info for every transitive package) and
// converts package-level diagnostics into a single error.
func loadPackages(ctx context.Context, dir string, patterns []string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Context: ctx,
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedTypesInfo |
			packages.NeedTypesSizes |
			packages.NeedModule,
		Dir: dir,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		// Use %w so callers can detect context.Canceled / context.DeadlineExceeded.
		return nil, fmt.Errorf("packages.Load: %w", err)
	}

	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages loaded for pattern %q", patterns[0])
	}

	var pkgErrs []error
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		for _, e := range pkg.Errors {
			pkgErrs = append(pkgErrs, e)
		}
	})
	if len(pkgErrs) > 0 {
		if toolchainVersionMismatch(pkgErrs) {
			return nil, fmt.Errorf(
				"toolchain version mismatch: trawl was compiled with an older Go version than the target module requires; "+
					"rebuild trawl with the current toolchain (go build -o trawl ./cmd/trawl): %w",
				ErrPackageLoad,
			)
		}
		return nil, fmt.Errorf("%w: %w", ErrPackageLoad, errors.Join(pkgErrs...))
	}
	return pkgs, nil
}

// modulePathOf returns the module path of the primary package (the one
// matching pattern), falling back to the first package that has module
// information. It is empty in GOPATH mode.
func modulePathOf(pkgs []*packages.Package, dir, pattern string) string {
	primary := findPrimaryPkgPath(pkgs, dir, pattern)
	for _, p := range pkgs {
		if p.PkgPath == primary && p.Module != nil {
			return p.Module.Path
		}
	}
	for _, p := range pkgs {
		if p.Module != nil {
			return p.Module.Path
		}
	}
	return ""
}

// buildGraph attaches the call graph selected by algo to result.
func buildGraph(ctx context.Context, result *LoadResult, algo Algo, pattern string) (*LoadResult, error) {
	prog := result.Prog
	switch algo {
	case AlgoRTA:
		// Graph is nil for RTA; the caller resolves an entry point and calls
		// rta.Analyze directly.
		return result, nil
	case AlgoCHA:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		graph := cha.CallGraph(prog)
		if graph == nil {
			return nil, fmt.Errorf("cha.CallGraph returned nil for pattern %q", pattern)
		}
		result.Graph = graph
		return result, nil
	case AlgoVTA, "":
		// proceed to VTA pipeline below
	default:
		return nil, fmt.Errorf("unknown algorithm %q: supported values are %q, %q, and %q", algo, AlgoVTA, AlgoRTA, AlgoCHA)
	}

	// Check cancellation before the VTA pipeline.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	initial := cha.CallGraph(prog)
	if initial == nil {
		return nil, fmt.Errorf("cha.CallGraph returned nil for pattern %q", pattern)
	}
	graph := vta.CallGraph(ssautil.AllFunctions(prog), initial)
	if graph == nil {
		return nil, fmt.Errorf("vta.CallGraph returned nil for pattern %q", pattern)
	}
	result.Graph = graph

	return result, nil
}

// resolveSSAPkg selects the SSA package to use as the analysis entry point.
// When no scope patterns are provided, it takes the first loaded package.
// Otherwise it locates the primary package by path, since scope loading may
// have placed additional packages ahead of it in ssaPkgs.
func resolveSSAPkg(ssaPkgs []*ssa.Package, pkgs []*packages.Package, dir, pattern string, scopePatterns []string) (*ssa.Package, error) {
	if len(scopePatterns) == 0 {
		if len(ssaPkgs) == 0 || ssaPkgs[0] == nil {
			return nil, fmt.Errorf("SSA package not found for %q", pattern)
		}
		return ssaPkgs[0], nil
	}
	primaryPath := findPrimaryPkgPath(pkgs, dir, pattern)
	if primaryPath == "" {
		return nil, fmt.Errorf("primary package not found for pattern %q among loaded packages", pattern)
	}
	for _, sp := range ssaPkgs {
		if sp != nil && sp.Pkg.Path() == primaryPath {
			return sp, nil
		}
	}
	return nil, fmt.Errorf("SSA package not found for %q", primaryPath)
}

// findPrimaryPkgPath returns the PkgPath of the package that matches pattern.
// It resolves pattern to an absolute directory and matches against each
// package's GoFiles location. Falls back to PkgPath equality for absolute
// import paths.
func findPrimaryPkgPath(pkgs []*packages.Package, dir, pattern string) string {
	wantDir, err := filepath.Abs(filepath.Join(dir, pattern))
	if err == nil {
		wantDir = filepath.Clean(wantDir)
		for _, pkg := range pkgs {
			if len(pkg.GoFiles) == 0 {
				continue
			}
			pkgDir := filepath.Dir(pkg.GoFiles[0])
			if pkgDir == wantDir {
				return pkg.PkgPath
			}
		}
	}
	// Fallback: exact PkgPath match (for absolute import paths).
	for _, pkg := range pkgs {
		if pkg.PkgPath == pattern {
			return pkg.PkgPath
		}
	}
	return ""
}

// toolchainVersionMismatch reports whether any of errs is a go/packages
// diagnostic about a Go toolchain version mismatch. These messages are emitted
// when the trawl binary was compiled with an older Go version than the
// environment's go list binary.
func toolchainVersionMismatch(errs []error) bool {
	for _, e := range errs {
		msg := e.Error()
		if strings.Contains(msg, "file requires newer Go version") ||
			strings.Contains(msg, "uses version go") {
			return true
		}
	}
	return false
}
