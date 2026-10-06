package walker_test

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/analysis"
	"github.com/shairoth12/trawl/internal/detector"
	"github.com/shairoth12/trawl/internal/walker"
)

// loadFixture loads a fixture under the module root with the given algorithm.
func loadFixture(t *testing.T, pattern string, algo analysis.Algo) *analysis.LoadResult {
	t.Helper()
	r, err := analysis.Load(t.Context(), analysis.Options{Dir: moduleRoot(t), Pattern: pattern, Algo: algo})
	if err != nil {
		t.Fatalf("analysis.Load(%q): %v", pattern, err)
	}
	return r
}

// resolve resolves entry in r or fails the test.
func resolve(t *testing.T, r *analysis.LoadResult, entry string) *ssa.Function {
	t.Helper()
	fn, err := analysis.Resolve(r, entry)
	if err != nil {
		t.Fatalf("analysis.Resolve(%q): %v", entry, err)
	}
	return fn
}

// loadCrossmodule loads the two-module fixture's service module.
func loadCrossmodule(t *testing.T, algo analysis.Algo, deps analysis.DependencyPolicy, maxDeps int, indicators []trawl.Indicator) *analysis.LoadResult {
	t.Helper()
	det := detector.New(indicators)
	dir := filepath.Join(moduleRoot(t), "testdata", "crossmodule", "svc")
	r, err := analysis.Load(t.Context(), analysis.Options{
		Dir: dir, Pattern: ".", Algo: algo, DependencyPolicy: deps, MaxDependencyPkgs: maxDeps,
		IsIndicator: func(p string) bool { _, ok := det.Detect(p); return ok },
	})
	if err != nil {
		t.Fatalf("analysis.Load(crossmodule): %v", err)
	}
	return r
}

// walkCrossmodule loads the fixture, resolves entry, and walks it with the
// given algorithm, dependency policy, cap (0 = default), and indicators.
func walkCrossmodule(t *testing.T, entry string, algo analysis.Algo, indicators []trawl.Indicator, deps analysis.DependencyPolicy, maxDeps int) ([]trawl.ExternalCall, walker.WalkStats) {
	t.Helper()
	r := loadCrossmodule(t, algo, deps, maxDeps, indicators)
	fn := resolve(t, r, entry)
	graph := r.Graph
	if algo == analysis.AlgoRTA {
		graph = rta.Analyze([]*ssa.Function{fn}, true).CallGraph
	}
	w := walker.New(graph, detector.New(indicators), walker.Options{Module: r.Module, DependencyPkgs: r.DependencyPkgs, Fset: r.Prog.Fset})
	calls, stats, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk(%q): %v", entry, err)
	}
	return calls, stats
}

func tracedOnly(calls []trawl.ExternalCall) []trawl.ExternalCall {
	var out []trawl.ExternalCall
	for _, ec := range calls {
		if ec.ResolvedVia == trawl.ResolvedViaCrossModuleTrace {
			out = append(out, ec)
		}
	}
	return out
}

func TestCalleePkg_GenericTopLevel(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	r := loadCrossmodule(t, analysis.AlgoCHA, analysis.DependencyAuto, 0, nil)
	var inst *ssa.Function
	for fn := range ssautil.AllFunctions(r.Prog) {
		if strings.HasPrefix(fn.String(), "example.com/lib/util.Map[") {
			inst = fn
			break
		}
	}
	if inst == nil {
		t.Fatal("no instantiation of util.Map found in program")
	}
	if inst.Package() != nil {
		t.Fatalf("precondition: instantiation has non-nil Package(); fixture no longer exercises the nil case")
	}
	path, pkg := walker.CalleePkg(inst)
	if path != "example.com/lib/util" || pkg == nil {
		t.Errorf("CalleePkg(%s) = (%q, %v), want (\"example.com/lib/util\", non-nil)", inst, path, pkg)
	}
}

func TestWalk_GenericEdgeDoesNotHideStoreEdge(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	calls, stats := walkCrossmodule(t, "HandleGeneric", analysis.AlgoCHA, nil, analysis.DependencyAuto, 0)
	if stats.EdgesExamined < 2 {
		t.Errorf("EdgesExamined = %d, want >= 2 (generic edge + interface edge)", stats.EdgesExamined)
	}
	var found bool
	for _, ec := range calls {
		if ec.ServiceType == trawl.ServiceTypePostgres {
			found = true
		}
	}
	if !found {
		t.Errorf("POSTGRES not detected from HandleGeneric; got %v", calls)
	}
}

func TestWalk_GenericCallIntoIndicatorIsDirect(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// util.Map[string, string] has no SSA package; it must still match the indicator.
	ind := []trawl.Indicator{{Package: "example.com/lib/util", ServiceType: trawl.ServiceType("UTIL")}}
	calls, _ := walkCrossmodule(t, "HandleGeneric", analysis.AlgoCHA, ind, analysis.DependencyAuto, 0)
	for _, ec := range calls {
		if ec.ServiceType == "UTIL" && ec.ResolvedVia == trawl.ResolvedViaDirect && ec.Confidence == trawl.ConfidenceHigh {
			return
		}
	}
	t.Errorf("Walk(HandleGeneric) with util as indicator: no UTIL direct/high record; got %+v", calls)
}

func TestWalk_CrossModule_VTA_InjectedInterfaceIsTraced(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// No value flow reaches Handler.Store under VTA (reflection-style DI), so
	// the invoke gets its CHA callees and the walk enters the dependency body.
	calls, stats := walkCrossmodule(t, "HandleGet", analysis.AlgoVTA, nil, analysis.DependencyAuto, 0)
	if stats.UnresolvedInvokes != 0 {
		t.Errorf("UnresolvedInvokes = %d, want 0", stats.UnresolvedInvokes)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want exactly 1 traced record; got %+v", len(calls), calls)
	}
	got := calls[0]
	if got.ResolvedVia != trawl.ResolvedViaCrossModuleTrace || got.ServiceType != trawl.ServiceTypePostgres || got.Confidence != trawl.ConfidenceHigh {
		t.Errorf("resolved_via/service/confidence = %s/%s/%s, want cross_module_trace/POSTGRES/high", got.ResolvedVia, got.ServiceType, got.Confidence)
	}
	if got.Function != "(*example.com/lib/store.sqlStore).Get" || got.ImportPath != "example.com/lib/store" {
		t.Errorf("function/import = %q/%q, want boundary callee and its package", got.Function, got.ImportPath)
	}
	if !strings.HasSuffix(got.File, filepath.Join("svc", "svc.go")) || got.Line == 0 {
		t.Errorf("file/line = %q:%d, want the call site in svc.go", got.File, got.Line)
	}
}

func TestWalk_SameModuleUnresolvedIsCountedOnly(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// scope/leaf without scope: Store.Get has no implementor in the program.
	r := loadFixture(t, "./testdata/scope/leaf", analysis.AlgoVTA)
	fn := resolve(t, r, "HandleLeaf")
	w := walker.New(r.Graph, detector.New(nil), walker.Options{Module: r.Module, Fset: r.Prog.Fset})
	calls, stats, err := w.Walk(fn)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 || stats.UnresolvedInvokes != 1 {
		t.Errorf("calls=%d unresolved=%d, want 0 and 1", len(calls), stats.UnresolvedInvokes)
	}
}

func TestWalk_StdlibInterfacesIgnored(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// chain fixture: resp.Body.Close() (io.ReadCloser) and w.WriteHeader
	// (http.ResponseWriter) have no callees under VTA and must neither be
	// reported nor counted.
	r := loadFixture(t, "./testdata/chain", analysis.AlgoVTA)
	fn := resolve(t, r, "HandleChain")
	w := walker.New(r.Graph, detector.New(nil), walker.Options{Module: r.Module, Fset: r.Prog.Fset})
	calls, stats, err := w.Walk(fn)
	if err != nil {
		t.Fatal(err)
	}
	if stats.UnresolvedInvokes != 0 {
		t.Errorf("UnresolvedInvokes = %d, want 0 (stdlib interfaces ignored)", stats.UnresolvedInvokes)
	}
	for _, ec := range calls {
		if ec.ResolvedVia == trawl.ResolvedViaInterfaceDispatch {
			t.Errorf("stdlib interface produced an abstract record: %+v", ec)
		}
	}
}

func TestWalk_CrossModule_TracesDependencyBody(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	calls, stats := walkCrossmodule(t, "HandleGet", analysis.AlgoCHA, nil, analysis.DependencyAuto, 0)
	traced := tracedOnly(calls)
	if len(traced) != 1 {
		t.Fatalf("traced records = %d, want 1; all calls: %+v", len(traced), calls)
	}
	got := traced[0]
	if got.ServiceType != trawl.ServiceTypePostgres || got.Confidence != trawl.ConfidenceHigh {
		t.Errorf("service/confidence = %s/%s, want POSTGRES/high", got.ServiceType, got.Confidence)
	}
	if got.Function != "(*example.com/lib/store.sqlStore).Get" || got.ImportPath != "example.com/lib/store" {
		t.Errorf("function/import = %q/%q, want boundary callee and its package", got.Function, got.ImportPath)
	}
	if !strings.HasSuffix(got.File, filepath.Join("svc", "svc.go")) || got.Line == 0 {
		t.Errorf("file/line = %q:%d, want the module-side call site in svc.go", got.File, got.Line)
	}
	if last := got.CallChain[len(got.CallChain)-1]; !strings.Contains(last, "database/sql") {
		t.Errorf("call chain must end at the backend call, got %v", got.CallChain)
	}
	if stats.TracedCrossings == 0 {
		t.Errorf("TracedCrossings = 0, want > 0")
	}
	// The MockStore edge lives in a built dependency package and must be
	// skipped, not inferred: exactly one record overall for this call site.
	if len(calls) != 1 {
		t.Errorf("total records = %d, want 1 (no mock-inferred duplicate); got %+v", len(calls), calls)
	}
}

func TestWalk_CrossModule_CompositeFacadePerMethod(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	fetch, _ := walkCrossmodule(t, "HandleFetch", analysis.AlgoCHA, nil, analysis.DependencyAuto, 0)
	for _, ec := range fetch {
		if ec.ServiceType != trawl.ServiceTypeHTTP {
			t.Errorf("HandleFetch reported %s, want only HTTP (facade must be classified per method); got %+v", ec.ServiceType, fetch)
		}
	}
	get, _ := walkCrossmodule(t, "HandleGet", analysis.AlgoCHA, nil, analysis.DependencyAuto, 0)
	for _, ec := range get {
		if ec.ServiceType != trawl.ServiceTypePostgres {
			t.Errorf("HandleGet reported %s, want only POSTGRES; got %+v", ec.ServiceType, get)
		}
	}
}

func TestWalk_CrossModule_SecondHopResolvedWhenBuilt(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// Round-2 selection builds lib/search, so Find → Searcher.Search → net/http resolves fully.
	calls, _ := walkCrossmodule(t, "HandleFind", analysis.AlgoCHA, nil, analysis.DependencyAuto, 0)
	traced := tracedOnly(calls)
	if len(traced) != 1 || traced[0].ServiceType != trawl.ServiceTypeHTTP || traced[0].Confidence != trawl.ConfidenceHigh {
		t.Fatalf("want one traced HTTP/high record through the built search client; got %+v", calls)
	}
}

func TestWalk_CrossModule_IndicatorInterfaceIsHighConfidence(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// lib/search is an indicator: never built, but the invoke on its interface is evidence.
	ind := []trawl.Indicator{{Package: "example.com/lib/search", ServiceType: trawl.ServiceType("SEARCH")}}
	calls, _ := walkCrossmodule(t, "HandleFind", analysis.AlgoCHA, ind, analysis.DependencyAuto, 0)
	traced := tracedOnly(calls)
	if len(traced) != 1 || traced[0].ServiceType != "SEARCH" || traced[0].Confidence != trawl.ConfidenceHigh {
		t.Fatalf("want one traced SEARCH/high record (interface declared in indicator package); got %+v", calls)
	}
	if last := traced[0].CallChain[len(traced[0].CallChain)-1]; last != "example.com/lib/search.Searcher.Search" {
		t.Errorf("chain must end at the interface label, got %q", last)
	}
}

func TestWalk_CrossModule_UnbuiltSecondHopInferredLow(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// Cap 2 keeps round-1 packages only; the Searcher invoke inside store is
	// unresolved and falls back to import inference on the search package.
	calls, _ := walkCrossmodule(t, "HandleFind", analysis.AlgoCHA, nil, analysis.DependencyAuto, 2)
	traced := tracedOnly(calls)
	if len(traced) != 1 || traced[0].ServiceType != trawl.ServiceTypeHTTP || traced[0].Confidence != trawl.ConfidenceLow {
		t.Fatalf("want one traced HTTP/low record; got %+v", calls)
	}
}

func TestWalk_CrossModule_TraceTerminatesOnCycle(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	calls, _ := walkCrossmodule(t, "HandlePing", analysis.AlgoCHA, nil, analysis.DependencyAuto, 0)
	traced := tracedOnly(calls)
	if len(traced) != 1 || traced[0].ServiceType != trawl.ServiceTypePostgres {
		t.Fatalf("want one traced POSTGRES record through pingA/pingB cycle; got %+v", calls)
	}
}

func TestWalk_CrossModule_DepNoneHasNoTraceRecords(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	calls, _ := walkCrossmodule(t, "HandleGet", analysis.AlgoCHA, nil, analysis.DependencyNone, 0)
	if n := len(tracedOnly(calls)); n != 0 {
		t.Errorf("trace records under DependencyNone = %d, want 0: %+v", n, calls)
	}
}

func TestWalk_CrossModule_UnresolvedSiteCountedOncePerWalk(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// Cap 2 leaves store's Searcher.Search invoke unresolved. Two module-side
	// calls reach it through two crossings: two records, but one unresolved site.
	calls, stats := walkCrossmodule(t, "HandleFindTwice", analysis.AlgoCHA, nil, analysis.DependencyAuto, 2)
	if n := len(tracedOnly(calls)); n != 2 {
		t.Errorf("traced records = %d, want 2 (one per module-side call site); got %+v", n, calls)
	}
	if stats.UnresolvedInvokes != 1 {
		t.Errorf("UnresolvedInvokes = %d, want 1 (one dependency-side site)", stats.UnresolvedInvokes)
	}
}
