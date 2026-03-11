// Package analysis loads a Go package, builds SSA form, and constructs a
// call graph using either VTA or RTA.
package analysis

import (
	"context"
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
type LoadResult struct {
	Prog   *ssa.Program
	Graph  *callgraph.Graph
	SSAPkg *ssa.Package // the directly-analyzed package (not transitive deps)
	Module string       // module path from go.mod, e.g. "github.com/foo/bar"
}

// Load loads the package at pattern (relative or import path) from dir,
// builds SSA, and constructs a call graph using the given algorithm.
// ctx is used for package loading cancellation and deadline propagation.
// For algo="rta", LoadResult.Graph is nil until the caller provides an entry
// point and calls rta.Analyze.
func Load(ctx context.Context, dir, pattern, algo string) (*LoadResult, error) {
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
		return nil, fmt.Errorf("packages.Load: %w", err)
	}

	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages loaded for pattern %s", pattern)
	}

	var errs []string
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		for _, e := range pkg.Errors {
			errs = append(errs, e.Error())
		}
	})
	if len(errs) > 0 {
		return nil, fmt.Errorf("package load errors:\n%s", strings.Join(errs, "\n"))
	}

	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	if len(ssaPkgs) == 0 || ssaPkgs[0] == nil {
		return nil, fmt.Errorf("SSA package not found for %s", pkgs[0].PkgPath)
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

	if algo == "rta" {
		// Graph is nil for RTA; the caller resolves an entry point and calls
		// rta.Analyze directly.
		return result, nil
	}

	// Default: VTA pipeline.
	initial := cha.CallGraph(prog)
	graph := vta.CallGraph(ssautil.AllFunctions(prog), initial)
	graph.DeleteSyntheticNodes()
	result.Graph = graph

	return result, nil
}
