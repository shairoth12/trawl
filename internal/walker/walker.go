// Package walker provides a DFS traversal of an SSA call graph that detects
// external service calls reachable from a given entry point.
//
// The walk follows calls through your module and into dependency packages
// whose bodies the analysis package built. Anything found inside a dependency
// is reported at the call in your code that led there. An interface call that
// resolves to no implementation is still reported, classified by the package
// that declares the interface. Several hits for one line are merged into one.
package walker

import (
	"fmt"
	"go/token"
	"go/types"
	"io"
	"log/slog"
	"slices"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/detector"
)

// WalkStats holds diagnostic counters collected during a single Walk call.
// The zero value is a valid, empty stats result.
type WalkStats struct {
	NodesVisited      int // functions entered during DFS; a dependency function counts again per call from your code that reaches it
	EdgesExamined     int // total outgoing edges considered (including skipped)
	UnresolvedInvokes int // interface calls that resolved to no implementation, or only to mocks (stdlib and very common interfaces excluded)
	TracedCrossings   int // times the walk went from your code into a dependency; one interface call with two dependency implementations counts twice
}

// Options configures a Walker.
type Options struct {
	Module         string          // module path prefix, e.g. "github.com/foo/bar" (LoadResult.Module); "" means every package counts as yours
	DependencyPkgs []string        // packages with built bodies the walk may enter (LoadResult.DependencyPkgs)
	Fset           *token.FileSet  // resolves source positions (LoadResult.Prog.Fset)
	Log            *slog.Logger    // debug-level edge decisions; nil disables logging
	Stdlib         map[string]bool // standard-library import paths (LoadResult.Stdlib); nil falls back to trawl.IsStandardLibrary
}

// Walker traverses a call graph from an entry point function and reports every
// external service call it can reach, in your module and in the dependency
// packages listed in Options.DependencyPkgs.
//
// Walker methods are not safe for concurrent use. Construct a new Walker per
// goroutine or add your own synchronization.
type Walker struct {
	graph      *callgraph.Graph
	det        detector.Detector
	module     string
	deps       map[string]bool
	stdlib     map[string]bool
	fset       *token.FileSet
	log        *slog.Logger
	inferCache map[*types.Package]trawl.ServiceType // guesses from imports depend only on the detector; never reset

	// Per-Walk state, reset at the start of each Walk call.
	visited         map[*callgraph.Node]bool
	countedSites    map[ssa.CallInstruction]bool // unresolved sites already counted; dependency nodes are re-walked per crossing
	edgesExamined   int
	depNodesVisited int
	tracedCrossings int
	unresolved      int
}

// hit is one detection plus the source position it is reported at; hits at the
// same position are merged later.
type hit struct {
	call trawl.ExternalCall
	pos  token.Pos
}

// crossing remembers the call in your code through which the walk entered a
// dependency. Everything found while inside is reported at that call.
type crossing struct {
	pos      token.Pos                // the call in your code
	function string                   // what it called, e.g. "(*example.com/lib/store.sqlStore).Get"
	pkgPath  string                   // package of that callee
	visited  map[*callgraph.Node]bool // functions already walked under this crossing
}

// New returns a Walker that will traverse graph and classify packages with det.
func New(graph *callgraph.Graph, d detector.Detector, opts Options) *Walker {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	deps := make(map[string]bool, len(opts.DependencyPkgs))
	for _, p := range opts.DependencyPkgs {
		deps[p] = true
	}
	return &Walker{
		graph:      graph,
		det:        d,
		module:     opts.Module,
		deps:       deps,
		stdlib:     opts.Stdlib,
		fset:       opts.Fset,
		log:        log,
		inferCache: map[*types.Package]trawl.ServiceType{},
	}
}

// Walk performs a DFS from entry and returns all external service calls
// reachable from it, along with diagnostic counters for the traversal.
//
// Walk returns an error if entry is not present in the call graph; this
// commonly happens with RTA when the entry point was not supplied as a root.
// The message suggests switching to VTA, which builds a complete call graph
// upfront.
//
// The returned slice is always non-nil, even when no external calls are found.
// WalkStats is zeroed on error.
func (w *Walker) Walk(entry *ssa.Function) ([]trawl.ExternalCall, WalkStats, error) {
	if w.graph == nil {
		return nil, WalkStats{}, fmt.Errorf("call graph is nil — did you forget to call rta.Analyze?")
	}
	node := w.graph.Nodes[entry]
	if node == nil {
		return nil, WalkStats{}, fmt.Errorf("entry function %s not found in call graph — try --algo vta", entry.String())
	}
	w.visited = make(map[*callgraph.Node]bool)
	w.countedSites = make(map[ssa.CallInstruction]bool)
	w.edgesExamined, w.depNodesVisited, w.tracedCrossings, w.unresolved = 0, 0, 0, 0

	hits := w.dfs(node, []string{entry.String()}, nil)
	stats := WalkStats{
		NodesVisited:      len(w.visited) + w.depNodesVisited,
		EdgesExamined:     w.edgesExamined,
		UnresolvedInvokes: w.unresolved,
		TracedCrossings:   w.tracedCrossings,
	}
	return mergeByPosition(hits), stats, nil
}

// dfs performs a depth-first traversal of the call graph starting at node.
// chain is the call chain accumulated so far (entry → … → current node).
// cross is nil in module code and non-nil inside dependency bodies.
func (w *Walker) dfs(node *callgraph.Node, chain []string, cross *crossing) []hit {
	visited := w.visited
	if cross != nil {
		visited = cross.visited
	}
	if visited[node] {
		return nil
	}
	visited[node] = true
	if cross != nil {
		w.depNodesVisited++
	}

	var hits []hit
	for _, edge := range node.Out {
		w.edgesExamined++
		fn := calleeFunc(edge)
		if fn == nil {
			continue
		}
		pkgPath, typesPkg := calleePkg(fn)
		if pkgPath == "" {
			w.log.Debug("skip_edge", "fn", fn.String(), "reason", "unknown_package")
			continue
		}
		// CHA resolves a call on a very common interface (error, fmt.Stringer,
		// io.Reader, ...) to every type in the program that has it: pure noise.
		if isUbiquitousDispatch(edge) {
			w.log.Debug("skip_edge", "pkg", pkgPath, "reason", "ubiquitous_dispatch")
			continue
		}
		next := appendCopy(chain, fn.String())

		if trawl.IsMockMethod(fn.Signature) {
			hits = append(hits, w.mockHits(edge, pkgPath, typesPkg, chain, cross)...)
			continue
		}
		// Detector check runs before the module-boundary check so that
		// third-party packages matching an indicator are reported rather than
		// silently skipped. After recording the call we do not recurse into
		// library internals.
		if svc, ok := w.det.Detect(pkgPath); ok {
			w.log.Debug("detect_hit", "pkg", pkgPath, "service_type", string(svc), "via", "direct")
			hits = append(hits, w.record(sitePos(edge), cross, svc, pkgPath, fn.String(), next, trawl.ResolvedViaDirect, trawl.ConfidenceHigh))
			continue
		}
		if w.inModule(pkgPath) {
			// Back in your code (also when a dependency calls back into it):
			// findings are reported at the line where they happen again.
			hits = append(hits, w.dfs(edge.Callee, next, nil)...)
			continue
		}
		if w.deps[pkgPath] {
			hits = append(hits, w.enterDependency(edge, fn, pkgPath, typesPkg, next, cross)...)
			continue
		}
		// External package without bodies, called through an interface: guess
		// the service from the package's imports (a wrapper imports the client).
		if isInvoke(edge) {
			if svc := w.inferFromTypesPkg(typesPkg); svc != "" {
				w.log.Debug("detect_hit", "pkg", pkgPath, "service_type", string(svc), "via", "cross_module_inference")
				hits = append(hits, w.record(sitePos(edge), cross, svc, pkgPath, fn.String(), next, trawl.ResolvedViaCrossModuleInference, trawl.ConfidenceLow))
				continue
			}
		}
		w.log.Debug("skip_edge", "pkg", pkgPath, "reason", "outside_module")
	}

	for _, site := range unresolvedInvokes(node) {
		if h, ok := w.unresolvedHit(site, chain, cross); ok {
			hits = append(hits, h)
		}
	}
	return hits
}

// record builds a hit. In your code (when cross is nil) the hit points at the
// call itself with the given resolvedVia. Inside a dependency (when cross is
// not nil) it points at the call in your code that entered it: file/line,
// function and import path come from cross, resolvedVia becomes
// cross_module_trace, and the confidence is that of what was actually found
// inside.
func (w *Walker) record(pos token.Pos, cross *crossing, svc trawl.ServiceType, importPath, function string, chain []string, resolvedVia, confidence string) hit {
	if cross == nil {
		return w.hitAt(pos, svc, importPath, function, chain, resolvedVia, confidence)
	}
	return w.hitAt(cross.pos, svc, cross.pkgPath, cross.function, chain, trawl.ResolvedViaCrossModuleTrace, confidence)
}

// enterDependency walks into a dependency package: it continues the current
// crossing, or opens a new one when coming from your code. If nothing is found
// inside and the call was through an interface, it falls back to guessing the
// service from the package's imports, so nothing that was detected before
// dependency bodies existed is lost.
func (w *Walker) enterDependency(edge *callgraph.Edge, fn *ssa.Function, pkgPath string, typesPkg *types.Package, chain []string, cross *crossing) []hit {
	if cross != nil {
		return w.dfs(edge.Callee, chain, cross)
	}
	w.tracedCrossings++
	newCross := &crossing{pos: sitePos(edge), function: fn.String(), pkgPath: pkgPath, visited: map[*callgraph.Node]bool{}}
	hits := w.dfs(edge.Callee, chain, newCross)
	found := slices.ContainsFunc(hits, func(h hit) bool { return h.pos == newCross.pos })
	if found || !isInvoke(edge) {
		return hits
	}
	if svc := w.inferFromTypesPkg(typesPkg); svc != "" {
		w.log.Debug("detect_hit", "pkg", pkgPath, "service_type", string(svc), "via", "cross_module_inference")
		hits = append(hits, w.hitAt(newCross.pos, svc, pkgPath, fn.String(), chain, trawl.ResolvedViaCrossModuleInference, trawl.ConfidenceLow))
	}
	return hits
}

// mockHits handles a call that resolved to a method on a mock. The mock
// is skipped whenever the real implementation is reachable: it lives in your
// module, or in a dependency package with bodies, or we are already inside a
// dependency. Otherwise, for a mock in an external package reached through an
// interface call, the service is guessed from the mock package's imports and
// the record is labeled with the interface method, not the mock type.
func (w *Walker) mockHits(edge *callgraph.Edge, pkgPath string, typesPkg *types.Package, chain []string, cross *crossing) []hit {
	if cross != nil || w.inModule(pkgPath) || w.deps[pkgPath] || !isInvoke(edge) {
		w.log.Debug("skip_edge", "pkg", pkgPath, "reason", "mock_method")
		return nil
	}
	svc := w.inferFromTypesPkg(typesPkg)
	if svc == "" {
		w.log.Debug("skip_edge", "pkg", pkgPath, "reason", "mock_method")
		return nil
	}
	label := interfaceMethodLabel(edge.Site.Common())
	resolvedVia, confidence := trawl.ResolvedViaMockInference, trawl.ConfidenceMedium
	// A mock whose package matches an indicator confirms the classification.
	if _, ok := w.det.Detect(pkgPath); ok {
		resolvedVia, confidence = trawl.ResolvedViaDirect, trawl.ConfidenceHigh
	}
	w.log.Debug("detect_hit", "pkg", pkgPath, "service_type", string(svc), "via", resolvedVia)
	return []hit{w.hitAt(sitePos(edge), svc, pkgPath, label, appendCopy(chain, label), resolvedVia, confidence)}
}

// unresolvedInvokes returns the interface calls in node's function that the
// call graph resolved to nothing, or only to mocks. A call whose only
// known callees are mocks is unresolved in the sense that matters: the real
// implementation is not in the program.
func unresolvedInvokes(node *callgraph.Node) []ssa.CallInstruction {
	if node.Func == nil || len(node.Func.Blocks) == 0 {
		return nil
	}
	resolved := make(map[ssa.CallInstruction]bool, len(node.Out))
	for _, e := range node.Out {
		if fn := calleeFunc(e); e.Site != nil && fn != nil && !trawl.IsMockMethod(fn.Signature) {
			resolved[e.Site] = true
		}
	}
	var out []ssa.CallInstruction
	for _, b := range node.Func.Blocks {
		for _, instr := range b.Instrs {
			site, ok := instr.(ssa.CallInstruction)
			if !ok || !site.Common().IsInvoke() || resolved[site] {
				continue
			}
			out = append(out, site)
		}
	}
	return out
}

// unresolvedHit decides what to report for an interface call that has no
// implementation, based on the package that declares the interface.
// error, standard-library and very common interfaces are ignored and not
// counted. If the package is an indicator the call is reported with high
// confidence; if it is another module, the service is guessed from its
// imports at low confidence; if it is your own module the call is counted
// but not reported. The bool is false when nothing is reported.
func (w *Walker) unresolvedHit(site ssa.CallInstruction, chain []string, cross *crossing) (hit, bool) {
	named, isNamed := types.Unalias(site.Common().Value.Type()).(*types.Named)
	if !isNamed || named.Obj().Pkg() == nil || isUbiquitousInterface(named) {
		return hit{}, false // builtin error, type parameters, ubiquitous interfaces
	}
	path := named.Obj().Pkg().Path()
	if w.isStdlib(path) {
		return hit{}, false
	}
	if !w.countedSites[site] {
		w.countedSites[site] = true
		w.unresolved++
	}
	label := interfaceMethodLabel(site.Common())
	if svc, isIndicator := w.det.Detect(path); isIndicator {
		return w.record(site.Pos(), cross, svc, path, label, appendCopy(chain, label), trawl.ResolvedViaInterfaceDispatch, trawl.ConfidenceHigh), true
	}
	if w.inModule(path) {
		w.log.Debug("unresolved_invoke", "method", label, "reason", "same_module_no_implementation")
		return hit{}, false
	}
	svc := w.inferFromTypesPkg(named.Obj().Pkg())
	if svc == "" {
		return hit{}, false
	}
	return w.record(site.Pos(), cross, svc, path, label, appendCopy(chain, label), trawl.ResolvedViaInterfaceDispatch, trawl.ConfidenceLow), true
}

// calleePkg returns the import path and *types.Package that own fn. For a
// generic instantiation fn.Package() is nil, so the package is taken from the
// generic declaration (Origin), then from the declaring object, then from the
// receiver's named type, whichever is found first.
func calleePkg(fn *ssa.Function) (string, *types.Package) {
	if p := fn.Package(); p != nil {
		return p.Pkg.Path(), p.Pkg
	}
	if o := fn.Origin(); o != nil && o.Package() != nil {
		return o.Package().Pkg.Path(), o.Package().Pkg
	}
	if obj := fn.Object(); obj != nil && obj.Pkg() != nil {
		return obj.Pkg().Path(), obj.Pkg()
	}
	if p := receiverTypesPkg(fn); p != nil {
		return p.Path(), p
	}
	return "", nil
}

// isStdlib reports whether pkgPath is a standard-library package, by
// Options.Stdlib when it was given and by the path rule otherwise.
func (w *Walker) isStdlib(pkgPath string) bool {
	if w.stdlib == nil {
		return trawl.IsStandardLibrary(pkgPath)
	}
	return w.stdlib[pkgPath]
}

// inModule reports whether pkgPath is inside the analyzed module: the module
// path itself or a path below it, so "example.com/app-extra" is not inside
// "example.com/app". An empty module (GOPATH workspace) places every package
// inside.
func (w *Walker) inModule(pkgPath string) bool {
	return w.module == "" || pkgPath == w.module || strings.HasPrefix(pkgPath, w.module+"/")
}

// calleeFunc returns the callee function of edge, or nil when the callee node
// or function is missing.
func calleeFunc(edge *callgraph.Edge) *ssa.Function {
	if edge.Callee == nil {
		return nil
	}
	return edge.Callee.Func
}

// isInvoke reports whether edge comes from a call through an interface.
func isInvoke(edge *callgraph.Edge) bool {
	return edge.Site != nil && edge.Site.Common().IsInvoke()
}

// sitePos returns the source position of the call behind edge, or token.NoPos
// when the edge has no call site (edges the call-graph builder made up).
func sitePos(edge *callgraph.Edge) token.Pos {
	if edge.Site == nil {
		return token.NoPos
	}
	return edge.Site.Pos()
}

// posAt resolves pos to a file name and line; both are zero for an invalid pos.
func (w *Walker) posAt(pos token.Pos) (string, int) {
	if !pos.IsValid() {
		return "", 0
	}
	p := w.fset.Position(pos)
	return p.Filename, p.Line
}

// hitAt builds a hit for a call at pos.
func (w *Walker) hitAt(pos token.Pos, svc trawl.ServiceType, importPath, function string, chain []string, resolvedVia, confidence string) hit {
	file, line := w.posAt(pos)
	return hit{pos: pos, call: trawl.ExternalCall{
		ServiceType: svc, ImportPath: importPath, Function: function,
		File: file, Line: line, CallChain: chain,
		ResolvedVia: resolvedVia, Confidence: confidence,
	}}
}

// interfaceMethodLabel returns "InterfaceType.MethodName" from an interface
// dispatch call site. The caller must ensure cc.IsInvoke() is true.
func interfaceMethodLabel(cc *ssa.CallCommon) string {
	return types.TypeString(cc.Value.Type(), nil) + "." + cc.Method.Name()
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
	"fmt.Stringer":       true,
	"io.Reader":          true,
	"io.Writer":          true,
	"io.Closer":          true,
	"io.ReadCloser":      true,
	"io.WriteCloser":     true,
	"io.ReadWriteCloser": true,
	"context.Context":    true,
	"sort.Interface":     true,
}

// isUbiquitousDispatch reports whether edge is an interface dispatch on a
// ubiquitous interface (error, fmt.Stringer, io.Reader, etc.). CHA resolves
// these dispatches to every implementing type in the program, producing
// false positives rather than meaningful service-type signals.
func isUbiquitousDispatch(edge *callgraph.Edge) bool {
	if !isInvoke(edge) {
		return false
	}
	return isUbiquitousInterface(edge.Site.Common().Value.Type())
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

// inferFromTypesPkg checks whether typesPkg imports a package the detector
// recognizes, directly or through one of its direct imports. Two levels are
// checked because a wrapper library often reaches the service client through
// one intermediate package. Results are cached per package.
func (w *Walker) inferFromTypesPkg(typesPkg *types.Package) trawl.ServiceType {
	if typesPkg == nil {
		return ""
	}
	if svc, ok := w.inferCache[typesPkg]; ok {
		return svc
	}
	var svc trawl.ServiceType
	for _, imp := range typesPkg.Imports() {
		svc = w.detectEither(imp)
		if svc != "" {
			break
		}
	}
	w.inferCache[typesPkg] = svc
	return svc
}

// detectEither classifies imp by its own path, then by its direct imports.
func (w *Walker) detectEither(imp *types.Package) trawl.ServiceType {
	if svc, ok := w.det.Detect(imp.Path()); ok {
		return svc
	}
	for _, imp2 := range imp.Imports() {
		if svc, ok := w.det.Detect(imp2.Path()); ok {
			return svc
		}
	}
	return ""
}

// receiverNamed returns the named type of fn's receiver (dereferencing a
// pointer receiver), or nil when fn has no receiver or an unnamed one.
func receiverNamed(fn *ssa.Function) *types.Named {
	recv := fn.Signature.Recv()
	if recv == nil {
		return nil
	}
	t := recv.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, _ := t.(*types.Named)
	return named
}

// receiverTypesPkg returns the *types.Package of fn's receiver type, or nil.
func receiverTypesPkg(fn *ssa.Function) *types.Package {
	named := receiverNamed(fn)
	if named == nil {
		return nil
	}
	return named.Obj().Pkg()
}
