package walker_test

import (
	"go/importer"
	"go/types"
	"path/filepath"
	"runtime"
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
	w := walker.New(result.Graph, det, result.Module, result.Prog.Fset, nil)
	calls, _, err := w.Walk(fn)
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
		if ec.ResolvedVia != trawl.ResolvedViaDirect {
			t.Errorf("ExternalCall.ResolvedVia = %q, want %q", ec.ResolvedVia, trawl.ResolvedViaDirect)
		}
		if ec.Confidence != trawl.ConfidenceHigh {
			t.Errorf("ExternalCall.Confidence = %q, want %q", ec.Confidence, trawl.ConfidenceHigh)
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
	w := walker.New(emptyResult.Graph, det, emptyResult.Module, emptyResult.Prog.Fset, nil)
	_, _, err = w.Walk(fn)
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
	w := walker.New(graph, det, result.Module, result.Prog.Fset, nil)
	calls, _, err := w.Walk(fn)
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

// importType imports pkgPath and returns the named type typeName from it.
func importType(t *testing.T, pkgPath, typeName string) types.Type {
	t.Helper()
	pkg, err := importer.Default().Import(pkgPath)
	if err != nil {
		t.Fatalf("importer.Default().Import(%q): %v", pkgPath, err)
	}
	obj := pkg.Scope().Lookup(typeName)
	if obj == nil {
		t.Fatalf("type %s.%s not found", pkgPath, typeName)
	}
	return obj.Type()
}

func TestIsUbiquitousInterface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  func(t *testing.T) types.Type
		want bool
	}{
		{
			name: "builtin_error",
			typ: func(t *testing.T) types.Type {
				t.Helper()
				return types.Universe.Lookup("error").Type()
			},
			want: true,
		},
		{
			name: "io_Reader",
			typ: func(t *testing.T) types.Type {
				t.Helper()
				return importType(t, "io", "Reader")
			},
			want: true,
		},
		{
			name: "fmt_Stringer",
			typ: func(t *testing.T) types.Type {
				t.Helper()
				return importType(t, "fmt", "Stringer")
			},
			want: true,
		},
		{
			name: "context_Context",
			typ: func(t *testing.T) types.Type {
				t.Helper()
				return importType(t, "context", "Context")
			},
			want: true,
		},
		{
			name: "sort_Interface",
			typ: func(t *testing.T) types.Type {
				t.Helper()
				return importType(t, "sort", "Interface")
			},
			want: true,
		},
		{
			name: "io_Writer",
			typ: func(t *testing.T) types.Type {
				t.Helper()
				return importType(t, "io", "Writer")
			},
			want: true,
		},
		{
			name: "custom_interface_not_ubiquitous",
			typ: func(t *testing.T) types.Type {
				t.Helper()
				pkg := types.NewPackage("example.com/mypkg", "mypkg")
				iface := types.NewInterfaceType(nil, nil)
				iface.Complete()
				name := types.NewTypeName(0, pkg, "MyIface", nil)
				named := types.NewNamed(name, iface, nil)
				return named
			},
			want: false,
		},
		{
			name: "non_Named_type",
			typ: func(t *testing.T) types.Type {
				t.Helper()
				iface := types.NewInterfaceType(nil, nil)
				iface.Complete()
				return iface
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ := tt.typ(t)
			got := walker.IsUbiquitousInterface(typ)
			if got != tt.want {
				t.Errorf("IsUbiquitousInterface(%v) = %v, want %v", typ, got, tt.want)
			}
		})
	}
}

func TestIsMockMethod(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	result, err := analysis.Load(t.Context(), root, "./testdata/mockfilter", analysis.AlgoCHA)
	if err != nil {
		t.Fatalf("analysis.Load(mockfilter, CHA): %v", err)
	}

	type fnCase struct {
		pattern string
		fn      *ssa.Function
		want    bool
	}
	cases := []fnCase{
		{pattern: ".MockStore).Get", want: true},
		{pattern: ".RealStore).Get", want: false},
		{pattern: ".HandleMock", want: false},
	}

	for fn := range ssautil.AllFunctions(result.Prog) {
		name := fn.String()
		for i := range cases {
			if strings.Contains(name, cases[i].pattern) {
				cases[i].fn = fn
			}
		}
	}

	for _, tc := range cases {
		if tc.fn == nil {
			t.Fatalf("SSA function matching %q not found", tc.pattern)
		}
		t.Run(tc.pattern, func(t *testing.T) {
			t.Parallel()
			got := walker.IsMockMethod(tc.fn)
			if got != tc.want {
				t.Errorf("IsMockMethod(%s) = %v, want %v", tc.fn, got, tc.want)
			}
			gotRecv := walker.IsMockReceiver(tc.fn)
			if gotRecv != tc.want {
				t.Errorf("IsMockReceiver(%s) = %v, want %v", tc.fn, gotRecv, tc.want)
			}
		})
	}
}

// walkFixtureCHA is like walkFixture but uses CHA instead of VTA, and accepts
// optional custom indicators for the detector.
func walkFixtureCHA(t *testing.T, pattern, entry string, indicators []trawl.Indicator) []trawl.ExternalCall {
	t.Helper()
	root := moduleRoot(t)
	result, err := analysis.Load(t.Context(), root, pattern, analysis.AlgoCHA)
	if err != nil {
		t.Fatalf("analysis.Load(%q, CHA): %v", pattern, err)
	}
	fn, err := analysis.Resolve(result, entry)
	if err != nil {
		t.Fatalf("analysis.Resolve(%q): %v", entry, err)
	}
	det := detector.New(indicators)
	w := walker.New(result.Graph, det, result.Module, result.Prog.Fset, nil)
	calls, _, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk(%q): %v", entry, err)
	}
	return calls
}

func TestWalk_GenericInterface_DirectDetection(t *testing.T) {
	t.Parallel()

	calls := walkFixtureCHA(t, "./testdata/generic", "HandleGeneric", nil)

	if len(calls) == 0 {
		t.Fatal("Walk returned no calls; expected at least one POSTGRES call")
	}

	var foundPostgres bool
	for _, ec := range calls {
		if ec.ServiceType == trawl.ServiceTypePostgres {
			foundPostgres = true
			if ec.ResolvedVia == trawl.ResolvedViaMockInference {
				t.Errorf("POSTGRES call resolved_via = %q, want %q or %q",
					ec.ResolvedVia, trawl.ResolvedViaDirect, trawl.ResolvedViaCrossModuleInference)
			}
		}
	}
	if !foundPostgres {
		t.Errorf("no POSTGRES call found; got %d calls: %v", len(calls), calls)
	}
}

func TestWalk_StatsNonZero(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	root := moduleRoot(t)
	result, err := analysis.Load(t.Context(), root, "./testdata/basic", analysis.AlgoVTA)
	if err != nil {
		t.Fatalf("analysis.Load: %v", err)
	}
	fn, err := analysis.Resolve(result, "HandleRequest")
	if err != nil {
		t.Fatalf("analysis.Resolve: %v", err)
	}
	det := detector.New(nil)
	w := walker.New(result.Graph, det, result.Module, result.Prog.Fset, nil)
	_, stats, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if stats.NodesVisited == 0 {
		t.Errorf("WalkStats.NodesVisited = 0, want > 0")
	}
	if stats.EdgesExamined == 0 {
		t.Errorf("WalkStats.EdgesExamined = 0, want > 0")
	}
}

func TestWalk_StatsReset(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	root := moduleRoot(t)
	result, err := analysis.Load(t.Context(), root, "./testdata/basic", analysis.AlgoVTA)
	if err != nil {
		t.Fatalf("analysis.Load: %v", err)
	}
	fn, err := analysis.Resolve(result, "HandleRequest")
	if err != nil {
		t.Fatalf("analysis.Resolve: %v", err)
	}
	det := detector.New(nil)
	w := walker.New(result.Graph, det, result.Module, result.Prog.Fset, nil)

	_, stats1, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk (first): %v", err)
	}
	_, stats2, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk (second): %v", err)
	}
	if stats1 != stats2 {
		t.Errorf("Walk called twice on same input: stats1=%+v, stats2=%+v, want equal", stats1, stats2)
	}
}

func TestInferFromTypesPkg(t *testing.T) {
	t.Parallel()

	det := detector.New(nil)

	tests := []struct {
		name       string
		importPath string
		want       trawl.ServiceType
	}{
		{"database_sql_matches_POSTGRES", "database/sql", trawl.ServiceTypePostgres},
		{"net_http_matches_HTTP", "net/http", trawl.ServiceTypeHTTP},
		{"fmt_no_match", "fmt", ""},
		{"net_http_httputil_transitive_HTTP", "net/http/httputil", trawl.ServiceTypeHTTP},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pkg, err := importer.Default().Import(tt.importPath)
			if err != nil {
				t.Fatalf("importer.Default().Import(%q): %v", tt.importPath, err)
			}
			got := walker.InferFromTypesPkg(det, pkg)
			if got != tt.want {
				t.Errorf("InferFromTypesPkg(%q) = %q, want %q", tt.importPath, got, tt.want)
			}
		})
	}
}
