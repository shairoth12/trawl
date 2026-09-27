// Package walker provides a DFS traversal of an SSA call graph that detects
// external service calls reachable from a given entry point.
//
// The walk recurses through module code and through dependency packages whose
// bodies were built by the analysis package; findings inside dependency code
// are attributed to the module-side call that entered it. Interface calls
// with no concrete callee are reported abstractly, classified by the package
// declaring the interface. Hits describing one call site are merged.
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
	NodesVisited      int // call graph node entries during DFS; dependency nodes count once per crossing
	EdgesExamined     int // total outgoing edges considered (including skipped)
	UnresolvedInvokes int // interface call sites with no concrete callee (stdlib and ubiquitous excluded)
	TracedCrossings   int // module-side call sites through which dependency bodies were entered
}

// Options configures a Walker.
type Options struct {
	Module         string         // module path prefix; "" recurses without a boundary
	DependencyPkgs []string       // non-initial packages with built bodies (LoadResult.DependencyPkgs)
	Fset           *token.FileSet // resolves source positions (LoadResult.Prog.Fset)
	Log            *slog.Logger   // debug-level edge decisions; nil disables logging
}

// Walker traverses a call graph from an entry point function and reports every
// external service call reachable within the module boundary.
//
// Walker methods are not safe for concurrent use. Construct a new Walker per
// goroutine or add your own synchronization.
type Walker struct {
	graph      *callgraph.Graph
	det        detector.Detector
	module     string
	deps       map[string]bool
	fset       *token.FileSet
	log        *slog.Logger
	inferCache map[*types.Package]trawl.ServiceType // import inference depends only on det; never reset

	// Per-Walk state, reset at the start of each Walk call.
	visited         map[*callgraph.Node]bool
	countedSites    map[ssa.CallInstruction]bool // unresolved sites already counted; dependency nodes are re-walked per crossing
	edgesExamined   int
	depNodesVisited int
	tracedCrossings int
	unresolved      int
}

// hit is a detection together with the call-site position it belongs to;
// the position drives merging.
type hit struct {
	call trawl.ExternalCall
	pos  token.Pos
}

// crossing describes the module-side call site through which the walk entered
// dependency code. Findings made while a crossing is active are attributed to
// that site.
type crossing struct {
	pos      token.Pos                // boundary call site in module code
	function string                   // boundary callee, e.g. "(*example.com/lib/store.sqlStore).Get"
	pkgPath  string                   // boundary callee's package
	visited  map[*callgraph.Node]bool // per-crossing visited set for dependency nodes
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
// cx is nil in module code and non-nil inside dependency bodies.
func (w *Walker) dfs(node *callgraph.Node, chain []string, cx *crossing) []hit {
	visited := w.visited
	if cx != nil {
		visited = cx.visited
	}
	if visited[node] {
		return nil
	}
	visited[node] = true
	if cx != nil {
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
		// CHA resolves dispatch on ubiquitous interfaces (error, fmt.Stringer,
		// io.Reader, ...) to every implementor in the program: pure noise.
		if isUbiquitousDispatch(edge) {
			w.log.Debug("skip_edge", "pkg", pkgPath, "reason", "ubiquitous_dispatch")
			continue
		}
		next := appendCopy(chain, fn.String())

		if isMockMethod(fn) {
			hits = append(hits, w.mockHits(edge, pkgPath, typesPkg, chain, cx)...)
			continue
		}
		// Detector check runs before the module-boundary check so that
		// third-party packages matching an indicator are reported rather than
		// silently skipped. After recording the call we do not recurse into
		// library internals.
		if svc, ok := w.det.Detect(pkgPath); ok {
			w.log.Debug("detect_hit", "pkg", pkgPath, "service_type", string(svc), "via", "direct")
			hits = append(hits, w.record(sitePos(edge), cx, svc, pkgPath, fn.String(), next, trawl.ResolvedViaDirect, trawl.ConfidenceHigh))
			continue
		}
		if w.inModule(pkgPath) {
			// Module code, including callbacks reached from dependency code:
			// attribution returns to the module-side call sites.
			hits = append(hits, w.dfs(edge.Callee, next, nil)...)
			continue
		}
		if w.deps[pkgPath] {
			hits = append(hits, w.enterDependency(edge, fn, pkgPath, typesPkg, next, cx)...)
			continue
		}
		// Body-less external callee reached through an interface: classify
		// from the package's imports (wrapper libraries import the client).
		if isInvoke(edge) {
			if svc := w.inferFromTypesPkg(typesPkg); svc != "" {
				w.log.Debug("detect_hit", "pkg", pkgPath, "service_type", string(svc), "via", "cross_module_inference")
				hits = append(hits, w.record(sitePos(edge), cx, svc, pkgPath, fn.String(), next, trawl.ResolvedViaCrossModuleInference, trawl.ConfidenceLow))
				continue
			}
		}
		w.log.Debug("skip_edge", "pkg", pkgPath, "reason", "outside_module")
	}

	for _, site := range unresolvedInvokes(node) {
		svc, path, confidence, ok := w.classifyUnresolved(site)
		if !ok {
			continue
		}
		label := interfaceMethodLabel(site.Common())
		hits = append(hits, w.record(site.Pos(), cx, svc, path, label, appendCopy(chain, label), trawl.ResolvedViaInterfaceDispatch, confidence))
	}
	return hits
}

// record builds a hit. In module code (cx == nil) the hit is the call's own
// site with the given resolution. Inside dependency code the hit is
// attributed to the boundary call site: file/line, function and import path
// come from the crossing, resolved_via is cross_module_trace, and the
// confidence is that of the evidence found.
func (w *Walker) record(pos token.Pos, cx *crossing, svc trawl.ServiceType, importPath, function string, chain []string, resolvedVia, confidence string) hit {
	if cx == nil {
		return w.hitAt(pos, svc, importPath, function, chain, resolvedVia, confidence)
	}
	return w.hitAt(cx.pos, svc, cx.pkgPath, cx.function, chain, trawl.ResolvedViaCrossModuleTrace, confidence)
}

// enterDependency continues an active crossing, or opens one from module
// code. When the dependency body yields no evidence, the legacy import
// inference for invoke edges applies so recall does not regress.
func (w *Walker) enterDependency(edge *callgraph.Edge, fn *ssa.Function, pkgPath string, typesPkg *types.Package, chain []string, cx *crossing) []hit {
	if cx != nil {
		return w.dfs(edge.Callee, chain, cx)
	}
	w.tracedCrossings++
	entered := &crossing{pos: sitePos(edge), function: fn.String(), pkgPath: pkgPath, visited: map[*callgraph.Node]bool{}}
	hits := w.dfs(edge.Callee, chain, entered)
	found := slices.ContainsFunc(hits, func(h hit) bool { return h.pos == entered.pos })
	if found || !isInvoke(edge) {
		return hits
	}
	if svc := w.inferFromTypesPkg(typesPkg); svc != "" {
		w.log.Debug("detect_hit", "pkg", pkgPath, "service_type", string(svc), "via", "cross_module_inference")
		hits = append(hits, w.hitAt(entered.pos, svc, pkgPath, fn.String(), chain, trawl.ResolvedViaCrossModuleInference, trawl.ConfidenceLow))
	}
	return hits
}

// mockHits handles an edge into a Mock*-typed method. Mocks are skipped when
// the real implementation is available — same-module mocks, mocks in a built
// dependency package, and any mock met inside dependency code. Remaining
// external mocks reached through an interface call are classified from their
// package's imports, labeled with the interface method.
func (w *Walker) mockHits(edge *callgraph.Edge, pkgPath string, typesPkg *types.Package, chain []string, cx *crossing) []hit {
	if cx != nil || w.inModule(pkgPath) || w.deps[pkgPath] || !isInvoke(edge) {
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

// unresolvedInvokes returns interface call sites in node's function that have
// no outgoing edge to a non-mock callee. A site whose only callees are mocks
// is unresolved in the sense that matters: the real implementor is missing
// from the program.
func unresolvedInvokes(node *callgraph.Node) []ssa.CallInstruction {
	if node.Func == nil || len(node.Func.Blocks) == 0 {
		return nil
	}
	resolved := make(map[ssa.CallInstruction]bool, len(node.Out))
	for _, e := range node.Out {
		if fn := calleeFunc(e); e.Site != nil && fn != nil && !isMockMethod(fn) {
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

// classifyUnresolved classifies an unresolved interface call by the package
// declaring the interface. Universe, stdlib and ubiquitous interfaces are
// ignored and not counted. An indicator package yields high confidence; an
// interface declared outside the module falls back to import inference at
// low confidence; a same-module interface is counted only.
func (w *Walker) classifyUnresolved(site ssa.CallInstruction) (svc trawl.ServiceType, importPath, confidence string, ok bool) {
	named, isNamed := types.Unalias(site.Common().Value.Type()).(*types.Named)
	if !isNamed || named.Obj().Pkg() == nil || isUbiquitousInterface(named) {
		return "", "", "", false // builtin error, type parameters, ubiquitous interfaces
	}
	path := named.Obj().Pkg().Path()
	if trawl.IsStandardLibrary(path) {
		return "", "", "", false
	}
	if !w.countedSites[site] {
		w.countedSites[site] = true
		w.unresolved++
	}
	if svc, ok := w.det.Detect(path); ok {
		return svc, path, trawl.ConfidenceHigh, true
	}
	if w.inModule(path) {
		w.log.Debug("unresolved_invoke", "iface", interfaceMethodLabel(site.Common()), "reason", "same_module_no_implementor")
		return "", "", "", false
	}
	if svc := w.inferFromTypesPkg(named.Obj().Pkg()); svc != "" {
		return svc, path, trawl.ConfidenceLow, true
	}
	return "", "", "", false
}

// calleePkg returns the import path and *types.Package owning fn. Generic
// instantiations have a nil Package(); the generic origin, the declaring
// object, or the receiver's named type supplies the package instead.
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

// inModule reports whether pkgPath is inside the analyzed module. An empty
// module (GOPATH workspace) places every package inside.
func (w *Walker) inModule(pkgPath string) bool {
	return w.module == "" || strings.HasPrefix(pkgPath, w.module)
}

// calleeFunc returns the callee function of edge, or nil when the callee node
// or function is missing.
func calleeFunc(edge *callgraph.Edge) *ssa.Function {
	if edge.Callee == nil {
		return nil
	}
	return edge.Callee.Func
}

// isInvoke reports whether edge originates at an interface method call.
func isInvoke(edge *callgraph.Edge) bool {
	return edge.Site != nil && edge.Site.Common().IsInvoke()
}

// sitePos returns the position of edge's call site, or token.NoPos for
// synthetic edges.
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

// isMockMethod reports whether fn is a method on a type whose name starts
// with "Mock". Mockery-generated mocks in production packages satisfy
// interfaces structurally, causing CHA to route through them. Mock bodies
// call testify/mock.Called() which fans out to the entire dependency graph.
func isMockMethod(fn *ssa.Function) bool {
	named := receiverNamed(fn)
	return named != nil && strings.HasPrefix(named.Obj().Name(), "Mock")
}

// inferFromTypesPkg checks whether typesPkg imports (directly or one level
// transitively) a package that the detector recognizes. Two levels are
// checked because wrapper libraries commonly wrap a service client through an
// intermediate package. Results are memoized per package.
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
