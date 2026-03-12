// Package walker provides a DFS traversal of an SSA call graph that detects
// external service calls reachable from a given entry point.
package walker

import (
	"fmt"
	"go/token"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/detector"
)

// Walker traverses a call graph from an entry point function and reports every
// external service call reachable within the module boundary.
//
// Walker methods are not safe for concurrent use. Construct a new Walker per
// goroutine or add your own synchronization.
type Walker struct {
	graph  *callgraph.Graph
	det    detector.Detector
	module string // module path prefix, e.g. "github.com/foo/bar"
	fset   *token.FileSet
}

// New returns a Walker that will traverse graph, classify packages with det,
// and limit recursion to packages whose import path starts with module.
//
// module comes from LoadResult.Module. If module is empty (GOPATH workspace),
// the walker recurses without a boundary.
// fset comes from LoadResult.Prog.Fset and is used to resolve source positions.
func New(graph *callgraph.Graph, d detector.Detector, module string, fset *token.FileSet) *Walker {
	return &Walker{graph: graph, det: d, module: module, fset: fset}
}

// Walk performs a DFS from entry and returns all external service calls
// reachable from it.
//
// Walk returns an error if entry is not present in the call graph; this
// commonly happens with RTA when the entry point was not supplied as a root.
// The message suggests switching to VTA, which builds a complete call graph
// upfront.
//
// The returned slice is always non-nil, even when no external calls are found.
func (w *Walker) Walk(entry *ssa.Function) ([]trawl.ExternalCall, error) {
	node := w.graph.Nodes[entry]
	if node == nil {
		return nil, fmt.Errorf("entry function %s not found in call graph — try --algo vta", entry.String())
	}
	visited := make(map[*callgraph.Node]bool)
	results := w.dfs(node, []string{entry.String()}, visited)
	if results == nil {
		results = []trawl.ExternalCall{}
	}
	return results, nil
}

// dfs performs a depth-first traversal of the call graph starting at node.
// chain is the call chain accumulated so far (entry → … → current node).
// visited prevents infinite loops on recursive or mutually-recursive calls.
func (w *Walker) dfs(
	node *callgraph.Node,
	chain []string,
	visited map[*callgraph.Node]bool,
) []trawl.ExternalCall {
	if visited[node] {
		return nil
	}
	visited[node] = true

	var results []trawl.ExternalCall
	for _, edge := range node.Out {
		callee := edge.Callee
		if callee == nil || callee.Func == nil {
			continue
		}
		fn := callee.Func
		pkg := fn.Package()
		if pkg == nil {
			continue // anonymous functions (lambdas) and some globals have no package
		}
		pkgPath := pkg.Pkg.Path()

		// Detector check runs before the module-boundary check so that
		// third-party packages matching an indicator are reported rather than
		// silently skipped. After recording the call we do not recurse into
		// library internals.
		if svcType, ok := w.det.Detect(pkgPath); ok {
			results = append(results, trawl.ExternalCall{
				ServiceType: svcType,
				ImportPath:  pkgPath,
				Function:    fn.RelString(pkg.Pkg),
				File:        w.posFile(edge),
				Line:        w.posLine(edge),
				CallChain:   appendCopy(chain, fn.String()),
			})
			continue
		}

		// Module boundary: only recurse into same-module packages.
		if !strings.HasPrefix(pkgPath, w.module) {
			continue
		}

		results = append(results, w.dfs(callee, appendCopy(chain, fn.String()), visited)...)
	}
	return results
}

// posFile returns the source file of the call site recorded in edge.
// Returns an empty string for synthetic edges (edge.Site == nil) or
// positions that are not valid.
func (w *Walker) posFile(edge *callgraph.Edge) string {
	if edge.Site == nil {
		return ""
	}
	pos := edge.Site.Pos()
	if !pos.IsValid() {
		return ""
	}
	return w.fset.Position(pos).Filename
}

// posLine returns the source line of the call site recorded in edge.
// Returns 0 for synthetic edges (edge.Site == nil) or invalid positions.
func (w *Walker) posLine(edge *callgraph.Edge) int {
	if edge.Site == nil {
		return 0
	}
	pos := edge.Site.Pos()
	if !pos.IsValid() {
		return 0
	}
	return w.fset.Position(pos).Line
}

// appendCopy returns a new slice with elem appended to chain.
// It always allocates, preventing DFS branches from corrupting each other's
// chains through Go's slice-aliasing semantics.
func appendCopy(chain []string, elem string) []string {
	out := make([]string, len(chain)+1)
	copy(out, chain)
	out[len(chain)] = elem
	return out
}
