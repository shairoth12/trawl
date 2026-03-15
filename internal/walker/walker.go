// Package walker provides a DFS traversal of an SSA call graph that detects
// external service calls reachable from a given entry point.
package walker

import (
	"fmt"
	"go/token"
	"go/types"
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
			// Generic instantiations lose their SSA package. For interface
			// dispatches to mock types (common with generic interfaces like
			// IRedisCache[T]), try to infer the service type from the
			// receiver type's package imports.
			if edge.Site != nil && edge.Site.Common().IsInvoke() && isMockReceiver(fn) {
				if svcType := w.inferFromReceiverPkg(fn); svcType != "" {
					results = append(results, trawl.ExternalCall{
						ServiceType: svcType,
						ImportPath:  receiverPkgPath(fn),
						Function:    fn.Name(),
						File:        w.posFile(edge),
						Line:        w.posLine(edge),
						CallChain:   appendCopy(chain, fn.String()),
					})
				}
			}
			continue
		}
		pkgPath := pkg.Pkg.Path()

		// Skip edges from dispatch on ubiquitous interfaces (error,
		// fmt.Stringer, io.Reader, etc.). CHA resolves these to every
		// implementor in the program, producing false positives.
		if isUbiquitousDispatch(edge) {
			continue
		}

		// Skip mock type methods. Mockery-generated mocks in production
		// packages satisfy interfaces structurally, causing CHA to route
		// through them into testify internals. For mocks in external
		// packages (outside the module boundary), infer the service type
		// from the mock's package imports — the mock lives alongside the
		// real implementation which imports the service library.
		if isMockMethod(fn) {
			if !strings.HasPrefix(pkgPath, w.module) {
				if edge.Site != nil && edge.Site.Common().IsInvoke() {
					if svcType := w.inferFromImports(pkg); svcType != "" {
						results = append(results, trawl.ExternalCall{
							ServiceType: svcType,
							ImportPath:  pkgPath,
							Function:    fn.RelString(pkg.Pkg),
							File:        w.posFile(edge),
							Line:        w.posLine(edge),
							CallChain:   appendCopy(chain, fn.String()),
						})
					}
				}
			}
			continue
		}

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
			// Cross-module inference: when CHA/VTA resolves an interface
			// dispatch to a concrete method in an external module, check
			// whether that module imports a known service library. This
			// handles wrapper packages (e.g., rediscache wrapping go-redis,
			// msgraph wrapping net/http).
			if edge.Site != nil && edge.Site.Common().IsInvoke() {
				if svcType := w.inferFromImports(pkg); svcType != "" {
					results = append(results, trawl.ExternalCall{
						ServiceType: svcType,
						ImportPath:  pkgPath,
						Function:    fn.RelString(pkg.Pkg),
						File:        w.posFile(edge),
						Line:        w.posLine(edge),
						CallChain:   appendCopy(chain, fn.String()),
					})
				}
			}
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

// ubiquitousInterfaces lists interfaces so widely implemented that CHA
// dispatch through them produces only noise, not meaningful service signals.
// The builtin error interface is handled separately (nil Pkg).
var ubiquitousInterfaces = map[string]bool{
	"fmt.Stringer":    true,
	"io.Reader":       true,
	"io.Writer":       true,
	"io.Closer":       true,
	"context.Context": true,
	"sort.Interface":  true,
}

// isUbiquitousDispatch reports whether edge is an interface dispatch on a
// ubiquitous interface (error, fmt.Stringer, io.Reader, etc.). CHA resolves
// these dispatches to every implementing type in the program, producing
// false positives rather than meaningful service-type signals.
func isUbiquitousDispatch(edge *callgraph.Edge) bool {
	if edge.Site == nil {
		return false
	}
	cc := edge.Site.Common()
	if !cc.IsInvoke() {
		return false
	}
	return isUbiquitousInterface(cc.Value.Type())
}

// isUbiquitousInterface reports whether t is a ubiquitous interface type.
func isUbiquitousInterface(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj.Pkg() == nil {
		return obj.Name() == "error"
	}
	return ubiquitousInterfaces[obj.Pkg().Path()+"."+obj.Name()]
}

// isMockMethod reports whether fn is a method on a type whose name starts
// with "Mock". Mockery-generated mocks in production packages satisfy
// interfaces structurally, causing CHA to route through them. Mock bodies
// call testify/mock.Called() which fans out to the entire dependency graph.
func isMockMethod(fn *ssa.Function) bool {
	recv := fn.Signature.Recv()
	if recv == nil {
		return false
	}
	t := recv.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return strings.HasPrefix(named.Obj().Name(), "Mock")
}

// inferFromImports checks whether ssaPkg imports (directly or one level
// transitively) a package that the detector recognizes. Two levels are checked
// because wrapper libraries commonly wrap a service client through an
// intermediate package (e.g., rediscache → infra/redis → go-redis).
func (w *Walker) inferFromImports(ssaPkg *ssa.Package) trawl.ServiceType {
	if ssaPkg == nil {
		return ""
	}
	typesPkg := ssaPkg.Pkg
	if typesPkg == nil {
		return ""
	}
	return w.inferFromTypesPkg(typesPkg)
}

// inferFromTypesPkg checks whether typesPkg imports (directly or one level
// transitively) a package that the detector recognizes.
func (w *Walker) inferFromTypesPkg(typesPkg *types.Package) trawl.ServiceType {
	if typesPkg == nil {
		return ""
	}
	for _, imp := range typesPkg.Imports() {
		if svcType, ok := w.det.Detect(imp.Path()); ok {
			return svcType
		}
		// Check one level deeper for double-wrapped libraries.
		for _, imp2 := range imp.Imports() {
			if svcType, ok := w.det.Detect(imp2.Path()); ok {
				return svcType
			}
		}
	}
	return ""
}

// isMockReceiver reports whether fn has a receiver whose named type starts
// with "Mock". Unlike isMockMethod, this works even when fn.Package() is nil
// (common for generic instantiations).
func isMockReceiver(fn *ssa.Function) bool {
	recv := fn.Signature.Recv()
	if recv == nil {
		return false
	}
	t := recv.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return strings.HasPrefix(named.Obj().Name(), "Mock")
}

// inferFromReceiverPkg infers a service type from the receiver type's package
// imports. This handles generic mock types where fn.Package() returns nil but
// the receiver's *types.Named still carries a valid package reference.
func (w *Walker) inferFromReceiverPkg(fn *ssa.Function) trawl.ServiceType {
	typesPkg := receiverTypesPkg(fn)
	if typesPkg == nil {
		return ""
	}
	return w.inferFromTypesPkg(typesPkg)
}

// receiverTypesPkg returns the *types.Package of fn's receiver type, or nil.
func receiverTypesPkg(fn *ssa.Function) *types.Package {
	recv := fn.Signature.Recv()
	if recv == nil {
		return nil
	}
	t := recv.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	return named.Obj().Pkg()
}

// receiverPkgPath returns the package path of fn's receiver type, or
// an empty string if it cannot be determined.
func receiverPkgPath(fn *ssa.Function) string {
	pkg := receiverTypesPkg(fn)
	if pkg == nil {
		return ""
	}
	return pkg.Path()
}
