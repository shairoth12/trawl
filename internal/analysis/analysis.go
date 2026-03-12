// Package analysis loads a Go package, builds SSA form, and constructs a
// call graph using either VTA or RTA.
package analysis

import (
	"context"
	"errors"
	"fmt"
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
func Load(ctx context.Context, dir, pattern string, algo Algo) (*LoadResult, error) {
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

	pkgs, err := packages.Load(cfg, pattern)
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

	if len(ssaPkgs) == 0 || ssaPkgs[0] == nil {
		return nil, fmt.Errorf("SSA package not found for %q", pkgs[0].PkgPath)
	}
	ssaPkg := ssaPkgs[0]

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
	case AlgoVTA, "":
		// proceed to VTA pipeline below
	default:
		return nil, fmt.Errorf("unknown algorithm %q: supported values are %q and %q", algo, AlgoVTA, AlgoRTA)
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
