// Package analysis loads a Go package, builds its SSA form, and constructs a
// call graph using VTA, RTA, or CHA.
//
// Besides the packages named by --pkg and --scope, function bodies are also
// built for the other packages of the analyzed module and for dependency
// packages that implement an interface the analyzed code calls. This lets the
// walker follow a call through an interface into its real implementation
// even when that implementation lives in another module.
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

	"github.com/shairoth12/trawl"
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

	// DependencyPkgs lists the extra packages whose function bodies were
	// built, beyond those named by --pkg and --scope: first the other packages
	// of the analyzed module (sorted by path), then packages from other
	// modules (in the round they were picked, then by path).
	DependencyPkgs []string

	// DependencyPkgsSkipped is how many other-module packages were left out
	// because of Options.MaxDependencyPkgs.
	DependencyPkgsSkipped int

	// PackagesAnalyzed is the number of packages with built bodies: the
	// --pkg/--scope packages plus len(DependencyPkgs).
	PackagesAnalyzed int
}

// DependencyPolicy selects which packages, beyond those named by --pkg and
// --scope, get their function bodies built.
type DependencyPolicy string

const (
	// DependencyAuto also builds bodies for the other packages of the analyzed
	// module and for dependency packages that implement an interface the
	// analyzed code calls.
	DependencyAuto DependencyPolicy = "auto"
	// DependencyNone builds bodies only for the --pkg and --scope packages
	// (the behavior before dependency bodies existed).
	DependencyNone DependencyPolicy = "none"
)

// DefaultMaxDependencyPkgs is the cap on other-module packages that get bodies
// under DependencyAuto when Options.MaxDependencyPkgs is zero. It is a safety
// limit for the rare case where a very generic interface (say Get(ctx, key))
// matches dozens of unrelated packages, not a measured optimum: it counts
// packages, not their size. Typical selections are far smaller. See
// docs/adr/0009-selective-dependency-bodies.md.
const DefaultMaxDependencyPkgs = 200

// Options configures Load.
type Options struct {
	Dir     string   // working directory for go/packages, typically the module root
	Pattern string   // package pattern to analyze, e.g. "." or "./cmd/server"
	Algo    Algo     // call graph algorithm; "" defaults to AlgoVTA
	Scope   []string // extra package patterns loaded as initial packages

	DependencyPolicy  DependencyPolicy // "" defaults to DependencyAuto
	MaxDependencyPkgs int              // cap on cross-module packages with bodies; 0 → DefaultMaxDependencyPkgs

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
// opts.Scope names extra packages to load with bodies so their types are
// visible. opts.DependencyPolicy decides whether more packages get bodies
// automatically (see DependencyPolicy); opts.MaxDependencyPkgs caps how many
// other-module packages that may be, and opts.IsIndicator excludes packages
// the detector already classifies, since the walk stops at those anyway.
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
	switch opts.DependencyPolicy {
	case "":
		opts.DependencyPolicy = DependencyAuto
	case DependencyAuto, DependencyNone:
	default:
		return nil, fmt.Errorf("unknown dependency policy %q: supported values are %q and %q", opts.DependencyPolicy, DependencyAuto, DependencyNone)
	}

	allPatterns := make([]string, 0, 1+len(opts.Scope))
	allPatterns = append(allPatterns, opts.Pattern)
	for _, scopePattern := range opts.Scope {
		scopePattern = strings.TrimSpace(scopePattern)
		if scopePattern != "" && scopePattern != opts.Pattern {
			allPatterns = append(allPatterns, scopePattern)
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
	if opts.DependencyPolicy == DependencyAuto {
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

// createProgram is a copy of ssautil.Packages with one change: ssautil gives
// function bodies only to the --pkg/--scope packages, while this also gives
// them to the packages in withBodies. Every package is created exactly once, and each
// package is created after the packages it imports.
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

// buildProgram builds every created package. It does not use Program.Build
// because that starts a goroutine per package, and a panic inside one of them
// cannot be caught here and would crash the tool. Instead each package is
// built by this function, at most GOMAXPROCS at a time, with any panic turned
// into an error. Package.Build is safe to call this way: it may run
// concurrently for different packages and does nothing the second time.
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

// recoverBuild runs build and turns a panic into an error that tells the user
// how to work around it (--deps none).
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
	fillEmptyInvokes(graph, initial)
	result.Graph = graph

	return result, nil
}

// fillEmptyInvokes gives each interface call that VTA resolved to nothing the
// callees CHA found for it. VTA only follows values through code, so an
// interface filled by reflection-based DI (dig, fx) has no callees; CHA
// matches by type and finds the implementations, including the ones in
// dependency packages whose bodies were built for this. Interfaces declared in
// the standard library, and error, are left alone: they match too many types
// and no dependency bodies are built for them.
func fillEmptyInvokes(graph, initial *callgraph.Graph) {
	resolved := map[ssa.CallInstruction]bool{}
	for _, n := range graph.Nodes {
		for _, edge := range n.Out {
			resolved[edge.Site] = true
		}
	}
	for fn, n := range initial.Nodes {
		for _, edge := range n.Out {
			if fn == nil || edge.Site == nil || resolved[edge.Site] || !invokesNonStdlibInterface(edge.Site) {
				continue
			}
			callgraph.AddEdge(graph.CreateNode(fn), edge.Site, graph.CreateNode(edge.Callee.Func))
		}
	}
}

// invokesNonStdlibInterface reports whether site calls a method on a named
// interface declared outside the standard library.
func invokesNonStdlibInterface(site ssa.CallInstruction) bool {
	cc := site.Common()
	if !cc.IsInvoke() {
		return false
	}
	named, ok := types.Unalias(cc.Value.Type()).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return !trawl.IsStandardLibrary(named.Obj().Pkg().Path())
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
