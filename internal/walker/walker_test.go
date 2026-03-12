package walker_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/ssa"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/analysis"
	"github.com/shairoth12/trawl/internal/detector"
	"github.com/shairoth12/trawl/internal/walker"
)

// moduleRoot returns the module root directory by locating the source file via
// runtime.Caller. This is robust regardless of the working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	// file: .../internal/walker/walker_test.go
	// Three Dir calls: walker/ → internal/ → module root
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// walkFixture loads the fixture at pattern from the module root, resolves entry,
// builds a default VTA-based walker, and returns the Walk results.
func walkFixture(t *testing.T, pattern, entry string) []trawl.ExternalCall {
	t.Helper()
	root := moduleRoot(t)
	result, err := analysis.Load(t.Context(), root, pattern, analysis.AlgoVTA)
	if err != nil {
		t.Fatalf("analysis.Load(%q): %v", pattern, err)
	}
	fn, err := analysis.Resolve(result, entry)
	if err != nil {
		t.Fatalf("analysis.Resolve(%q): %v", entry, err)
	}
	det := detector.New(nil)
	w := walker.New(result.Graph, det, result.Module, result.Prog.Fset)
	calls, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk(%q): %v", entry, err)
	}
	return calls
}

func TestWalk_Direct(t *testing.T) {
	t.Parallel()

	calls := walkFixture(t, "./testdata/basic", "HandleRequest")

	if len(calls) == 0 {
		t.Fatalf("Walk(HandleRequest) = 0 calls, want at least 1")
	}

	for _, ec := range calls {
		if ec.ServiceType != trawl.ServiceTypeHTTP {
			t.Errorf("ExternalCall.ServiceType = %q, want %q", ec.ServiceType, trawl.ServiceTypeHTTP)
		}
		if ec.Line == 0 {
			t.Errorf("ExternalCall.Line = 0, want non-zero (source position should be resolved)")
		}
		if len(ec.CallChain) < 2 {
			t.Errorf("ExternalCall.CallChain length = %d, want >= 2", len(ec.CallChain))
		}
	}
}

func TestWalk_Chain(t *testing.T) {
	t.Parallel()

	calls := walkFixture(t, "./testdata/chain", "HandleChain")

	if len(calls) == 0 {
		t.Fatalf("Walk(HandleChain) = 0 calls, want at least 1")
	}

	// At least one detected call should have a chain passing through the
	// service and repository layers — i.e., ≥3 entries.
	hasDeepChain := false
	for _, ec := range calls {
		if len(ec.CallChain) >= 3 {
			hasDeepChain = true
			break
		}
	}
	if !hasDeepChain {
		t.Errorf("Walk(HandleChain): no ExternalCall with CallChain length >= 3; got %v", calls)
	}
}

func TestWalk_Iface(t *testing.T) {
	t.Parallel()

	// VTA is required here: HTTPCaller flows through the Caller interface across
	// a function boundary, so only VTA's inter-procedural type propagation can
	// resolve the concrete callee.
	calls := walkFixture(t, "./testdata/iface", "HandleIface")

	if len(calls) == 0 {
		t.Fatalf("Walk(HandleIface) = 0 calls, want at least 1 (VTA must resolve HTTPCaller)")
	}
	for _, ec := range calls {
		if ec.ServiceType != trawl.ServiceTypeHTTP {
			t.Errorf("ExternalCall.ServiceType = %q, want %q", ec.ServiceType, trawl.ServiceTypeHTTP)
		}
	}
}

func TestWalk_Multi(t *testing.T) {
	t.Parallel()

	calls := walkFixture(t, "./testdata/multi", "HandleMulti")

	// HandleMulti reaches both net/http and database/sql.
	if len(calls) < 2 {
		t.Fatalf("Walk(HandleMulti) = %d calls, want >= 2", len(calls))
	}

	seen := make(map[trawl.ServiceType]bool)
	for _, ec := range calls {
		seen[ec.ServiceType] = true
	}
	if !seen[trawl.ServiceTypeHTTP] {
		t.Errorf("Walk(HandleMulti): HTTP service type not detected; got service types %v", seen)
	}
	if !seen[trawl.ServiceTypePostgres] {
		t.Errorf("Walk(HandleMulti): POSTGRES service type not detected; got service types %v", seen)
	}
}

func TestWalk_Cycle(t *testing.T) {
	t.Parallel()

	// HandleCycle exercises the visited-set guard: processA↔processB are
	// mutually recursive in the static call graph. Walk must not loop
	// indefinitely and must still report the HTTP call reachable via fetch.
	calls := walkFixture(t, "./testdata/cycle", "HandleCycle")

	if len(calls) == 0 {
		t.Fatalf("Walk(HandleCycle) = 0 calls, want at least 1 (HTTP call reachable via fetch)")
	}
	for _, ec := range calls {
		if ec.ServiceType != trawl.ServiceTypeHTTP {
			t.Errorf("ExternalCall.ServiceType = %q, want %q", ec.ServiceType, trawl.ServiceTypeHTTP)
		}
	}
}

func TestWalk_Goroutine(t *testing.T) {
	t.Parallel()

	// HandleAsync makes its HTTP call inside a go func(){} closure. The call
	// graph includes an edge to the closure body, so Walk should follow it.
	calls := walkFixture(t, "./testdata/goroutine", "HandleAsync")

	if len(calls) == 0 {
		t.Fatalf("Walk(HandleAsync) = 0 calls, want at least 1 (HTTP call is inside go func)")
	}
	for _, ec := range calls {
		if ec.ServiceType != trawl.ServiceTypeHTTP {
			t.Errorf("ExternalCall.ServiceType = %q, want %q", ec.ServiceType, trawl.ServiceTypeHTTP)
		}
	}
}

func TestWalk_EntryNotInGraph(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	result, err := analysis.Load(t.Context(), root, "./testdata/basic", analysis.AlgoVTA)
	if err != nil {
		t.Fatalf("analysis.Load: %v", err)
	}
	fn, err := analysis.Resolve(result, "HandleRequest")
	if err != nil {
		t.Fatalf("analysis.Resolve: %v", err)
	}

	// Build a fresh empty graph so that fn is absent.
	emptyResult, err := analysis.Load(t.Context(), root, "./testdata/chain", analysis.AlgoVTA)
	if err != nil {
		t.Fatalf("analysis.Load(chain): %v", err)
	}

	det := detector.New(nil)
	w := walker.New(emptyResult.Graph, det, emptyResult.Module, emptyResult.Prog.Fset)
	_, err = w.Walk(fn)
	if err == nil {
		t.Fatalf("Walk(fn from different graph) = nil error, want an error about entry not found")
	}
}

func TestWalk_RTA(t *testing.T) {
	t.Parallel()

	// Confirm the RTA pipeline: Load with AlgoRTA leaves Graph nil; the caller
	// must resolve an entry point and call rta.Analyze to produce the graph.
	root := moduleRoot(t)
	result, err := analysis.Load(t.Context(), root, "./testdata/basic", analysis.AlgoRTA)
	if err != nil {
		t.Fatalf("analysis.Load(AlgoRTA): %v", err)
	}
	if result.Graph != nil {
		t.Fatal("AlgoRTA: LoadResult.Graph != nil, want nil before rta.Analyze")
	}

	fn, err := analysis.Resolve(result, "HandleRequest")
	if err != nil {
		t.Fatalf("analysis.Resolve: %v", err)
	}

	rtaResult := rta.Analyze([]*ssa.Function{fn}, true)
	graph := rtaResult.CallGraph

	det := detector.New(nil)
	w := walker.New(graph, det, result.Module, result.Prog.Fset)
	calls, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk(HandleRequest, RTA): %v", err)
	}
	if len(calls) == 0 {
		t.Fatalf("Walk(HandleRequest, RTA) = 0 calls, want at least 1")
	}
	for _, ec := range calls {
		if ec.ServiceType != trawl.ServiceTypeHTTP {
			t.Errorf("ExternalCall.ServiceType = %q, want %q", ec.ServiceType, trawl.ServiceTypeHTTP)
		}
	}
}
