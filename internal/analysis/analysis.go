// Package analysis loads a Go package, builds SSA form, and constructs a
// call graph using either VTA or RTA.
package analysis

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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

// Load loads the package at pattern from dir, builds SSA form, and constructs
// a call graph using the given algorithm.
//
// dir is the working directory passed to go/packages (typically the module root).
// pattern is the package load pattern (e.g. ".", "./cmd/server").
// algo selects the call graph algorithm; see AlgoVTA and AlgoRTA constants.
// An empty string defaults to AlgoVTA.
//
// For AlgoRTA, LoadResult.Graph is nil; the caller must resolve an entry point
// and call rta.Analyze to produce the graph.
//
// ctx is propagated into package loading and checked before each expensive
// phase. Cancelling ctx will abort Load early.
func Load(ctx context.Context, dir, pattern string, algo Algo, scopePatterns ...string) (*LoadResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("ctx must not be nil")
	}

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

	allPatterns := make([]string, 0, 1+len(scopePatterns))
	allPatterns = append(allPatterns, pattern)
	for _, sp := range scopePatterns {
		sp = strings.TrimSpace(sp)
		if sp != "" && sp != pattern {
			allPatterns = append(allPatterns, sp)
		}
	}
	pkgs, err := packages.Load(cfg, allPatterns...)
	if err != nil {
		// Use %w so callers can detect context.Canceled / context.DeadlineExceeded.
		return nil, fmt.Errorf("packages.Load: %w", err)
	}

	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages loaded for pattern %q", pattern)
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

	// Check cancellation before the expensive SSA build.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	prog.Build() // Build has no error return; panics on internal SSA errors.

	var ssaPkg *ssa.Package
	if len(scopePatterns) == 0 {
		if len(ssaPkgs) == 0 || ssaPkgs[0] == nil {
			return nil, fmt.Errorf("SSA package not found for %q", pattern)
		}
		ssaPkg = ssaPkgs[0]
	} else {
		primaryPath := findPrimaryPkgPath(pkgs, dir, pattern)
		if primaryPath == "" {
			return nil, fmt.Errorf("primary package not found for pattern %q among loaded packages", pattern)
		}
		for _, sp := range ssaPkgs {
			if sp != nil && sp.Pkg.Path() == primaryPath {
				ssaPkg = sp
				break
			}
		}
		if ssaPkg == nil {
			return nil, fmt.Errorf("SSA package not found for %q", primaryPath)
		}
	}

	var modulePath string
	for _, pkg := range pkgs {
		if pkg.Module != nil {
			modulePath = pkg.Module.Path
			break
		}
	}

	result := &LoadResult{
		Prog:   prog,
		SSAPkg: ssaPkg,
		Module: modulePath,
	}

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
