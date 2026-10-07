// Package trawl_test contains end-to-end integration tests for the full
// analysis pipeline: load → resolve → walk → JSON result.
package trawl_test

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/ssa"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/analysis"
	"github.com/shairoth12/trawl/internal/detector"
	"github.com/shairoth12/trawl/internal/walker"
)

// moduleRoot returns the module root by resolving the path of this source file.
// Robust to any working directory at test time.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Dir(file)
}

// crossmoduleSvcDir returns the root of the two-module fixture's service module.
func crossmoduleSvcDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "testdata", "crossmodule", "svc")
}

// pipeline runs the full analysis pipeline for a fixture under the module
// root and returns the trawl.Result.
func pipeline(
	t *testing.T,
	pattern, entryName string,
	indicators []trawl.Indicator,
	algo analysis.Algo,
	scope ...string,
) trawl.Result {
	t.Helper()
	return pipelineInDir(t, moduleRoot(t), pattern, entryName, indicators, algo, scope...)
}

// pipelineInDir runs pipelineOpts for a fixture that is its own Go module.
func pipelineInDir(t *testing.T, dir, pattern, entryName string, indicators []trawl.Indicator, algo analysis.Algo, scope ...string) trawl.Result {
	t.Helper()
	return pipelineOpts(t, analysis.Options{Dir: dir, Pattern: pattern, Algo: algo, Scope: scope}, entryName, indicators)
}

// pipelineOpts runs the full analysis pipeline with explicit load options and
// returns the trawl.Result. It fails the test immediately on any internal error.
func pipelineOpts(t *testing.T, opts analysis.Options, entryName string, indicators []trawl.Indicator) trawl.Result {
	t.Helper()

	loadResult, err := analysis.Load(t.Context(), opts)
	if err != nil {
		t.Fatalf("analysis.Load(%q): %v", opts.Pattern, err)
	}

	fn, err := analysis.Resolve(loadResult, entryName)
	if err != nil {
		t.Fatalf("analysis.Resolve(%q): %v", entryName, err)
	}

	graph := loadResult.Graph
	if opts.Algo == analysis.AlgoRTA {
		rtaResult := rta.Analyze([]*ssa.Function{fn}, true)
		graph = rtaResult.CallGraph
	}

	det := detector.New(indicators)
	w := walker.New(graph, det, walker.Options{Module: loadResult.Module, DependencyPkgs: loadResult.DependencyPkgs, Stdlib: loadResult.Stdlib, Fset: loadResult.Prog.Fset})
	calls, _, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk(%q): %v", entryName, err)
	}

	return trawl.Result{
		EntryPoint:    fn.String(),
		Package:       loadResult.SSAPkg.Pkg.Path(),
		ExternalCalls: calls,
	}
}

func TestIntegration_Basic(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/basic", "HandleRequest", nil, analysis.AlgoVTA)

	if len(out.ExternalCalls) == 0 {
		t.Fatalf("pipeline(basic) = 0 external calls, want at least 1")
	}
	for _, ec := range out.ExternalCalls {
		if ec.ServiceType != trawl.ServiceTypeHTTP {
			t.Errorf("ExternalCall.ServiceType = %q, want %q", ec.ServiceType, trawl.ServiceTypeHTTP)
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
		if ec.ShortFunction == "" {
			t.Error("ExternalCall.ShortFunction is empty, want non-empty")
		}
		if len(ec.ShortFunction) > len(ec.Function) {
			t.Errorf("ShortFunction %q is longer than Function %q", ec.ShortFunction, ec.Function)
		}
		if len(ec.ShortCallChain) != len(ec.CallChain) {
			t.Errorf("ShortCallChain length = %d, want %d (same as CallChain)", len(ec.ShortCallChain), len(ec.CallChain))
		}
		if !strings.Contains(ec.Function, "/") && ec.ShortFunction != ec.Function {
			t.Errorf("ShortFunction %q != Function %q (no path to strip)", ec.ShortFunction, ec.Function)
		}
	}
}

func TestIntegration_DeepCallChain(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/chain", "HandleChain", nil, analysis.AlgoVTA)

	if len(out.ExternalCalls) == 0 {
		t.Fatalf("pipeline(chain) = 0 external calls, want at least 1")
	}

	// At least one detected call must pass through all three layers (handler →
	// service → repository → external), yielding a chain of length >= 3.
	hasDeep := false
	for _, ec := range out.ExternalCalls {
		if len(ec.CallChain) >= 3 {
			hasDeep = true
			break
		}
	}
	if !hasDeep {
		t.Errorf("pipeline(chain): no external call with CallChain length >= 3; got %v", out.ExternalCalls)
	}
}

func TestIntegration_MultiServiceTypes(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/multi", "HandleMulti", nil, analysis.AlgoVTA)

	if len(out.ExternalCalls) < 2 {
		t.Fatalf("pipeline(multi) = %d external calls, want >= 2", len(out.ExternalCalls))
	}

	seen := make(map[trawl.ServiceType]bool)
	for _, ec := range out.ExternalCalls {
		seen[ec.ServiceType] = true
	}
	if !seen[trawl.ServiceTypeHTTP] {
		t.Errorf("pipeline(multi): HTTP not detected; saw %v", seen)
	}
	if !seen[trawl.ServiceTypePostgres] {
		t.Errorf("pipeline(multi): POSTGRES not detected; saw %v", seen)
	}
}

func TestIntegration_CustomIndicatorOverridesBuiltin(t *testing.T) {
	t.Parallel()

	// A user indicator for "database/sql" with a custom type takes precedence
	// over the built-in POSTGRES indicator for the same prefix.
	custom := []trawl.Indicator{
		{Package: "database/sql", ServiceType: trawl.ServiceType("MYSQL")},
	}
	out := pipeline(t, "./testdata/multi", "HandleMulti", custom, analysis.AlgoVTA)

	seen := make(map[trawl.ServiceType]bool)
	for _, ec := range out.ExternalCalls {
		seen[ec.ServiceType] = true
	}
	if !seen[trawl.ServiceType("MYSQL")] {
		t.Errorf("pipeline(multi, custom): MYSQL not detected; saw %v", seen)
	}
	if seen[trawl.ServiceTypePostgres] {
		t.Errorf("pipeline(multi, custom): POSTGRES detected despite MYSQL override; saw %v", seen)
	}
}

func TestIntegration_JSONOutput(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/basic", "HandleRequest", nil, analysis.AlgoVTA)

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal(Result) error: %v", err)
	}

	// Round-trip through JSON must be lossless.
	var got trawl.Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(Result) error: %v", err)
	}
	if diff := cmp.Diff(out, got); diff != "" {
		t.Errorf("JSON round-trip mismatch (-want +got):\n%s", diff)
	}

	// Verify all required top-level keys are present.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parsing JSON as map: %v", err)
	}
	for _, key := range []string{"entry_point", "package", "external_calls"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("JSON output missing required key %q", key)
		}
	}
}

func TestIntegration_RTA(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/basic", "HandleRequest", nil, analysis.AlgoRTA)

	if len(out.ExternalCalls) == 0 {
		t.Fatalf("pipeline(basic, RTA) = 0 external calls, want at least 1")
	}
	for _, ec := range out.ExternalCalls {
		if ec.ServiceType != trawl.ServiceTypeHTTP {
			t.Errorf("ExternalCall.ServiceType = %q, want %q", ec.ServiceType, trawl.ServiceTypeHTTP)
		}
	}
}

func TestIntegration_ScopeResolvesInjectedInterface(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/scope/leaf", "HandleLeaf", nil, analysis.AlgoVTA,
		"./testdata/scope/...")

	if len(out.ExternalCalls) == 0 {
		t.Fatalf("pipeline(scope/leaf, VTA, scope) = 0 external calls, want >= 1")
	}
	seen := make(map[trawl.ServiceType]bool, len(out.ExternalCalls))
	for _, ec := range out.ExternalCalls {
		seen[ec.ServiceType] = true
	}
	if !seen[trawl.ServiceTypePostgres] {
		t.Errorf("POSTGRES not detected; saw %v", seen)
	}
}

func TestIntegration_NoScopeMissesInjectedInterface(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/scope/leaf", "HandleLeaf", nil, analysis.AlgoVTA)

	if len(out.ExternalCalls) != 0 {
		t.Errorf("pipeline(scope/leaf, VTA, no scope) = %d external calls, want 0",
			len(out.ExternalCalls))
	}
}

func TestIntegration_CHA_ResolvesReflectionDI(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/scope/leaf", "HandleLeaf", nil, analysis.AlgoCHA,
		"./testdata/scope/...")

	if len(out.ExternalCalls) == 0 {
		t.Fatalf("pipeline(scope/leaf, CHA, scope) = 0 external calls, want >= 1")
	}
	seen := make(map[trawl.ServiceType]bool, len(out.ExternalCalls))
	for _, ec := range out.ExternalCalls {
		seen[ec.ServiceType] = true
	}
	if !seen[trawl.ServiceTypePostgres] {
		t.Errorf("POSTGRES not detected via CHA; saw %v", seen)
	}
}

func TestIntegration_CHA_UbiquitousDispatchFiltered(t *testing.T) {
	t.Parallel()

	// A custom indicator matches the svcpkg subpackage. CHA would normally
	// resolve err.Error() to SvcError.Error() in svcpkg, triggering a false
	// positive. The ubiquitous-interface filter must suppress it while
	// preserving the real HTTP detection from doWork.
	custom := []trawl.Indicator{
		{
			Package:     "github.com/shairoth12/trawl/testdata/erriface/svcpkg",
			ServiceType: trawl.ServiceType("CUSTOMSVC"),
		},
	}
	out := pipeline(t, "./testdata/erriface", "HandleErr", custom, analysis.AlgoCHA,
		"./testdata/erriface/...")

	seen := make(map[trawl.ServiceType]bool, len(out.ExternalCalls))
	for _, ec := range out.ExternalCalls {
		seen[ec.ServiceType] = true
	}
	if !seen[trawl.ServiceTypeHTTP] {
		t.Errorf("HTTP not detected; saw %v", seen)
	}
	if seen[trawl.ServiceType("CUSTOMSVC")] {
		t.Errorf("CUSTOMSVC detected via error.Error() dispatch — ubiquitous filter failed; saw %v", seen)
	}
}

func TestIntegration_CHA_MockFilterSuppressesFalsePositive(t *testing.T) {
	t.Parallel()

	// CHA resolves Store.Get to both MockStore.Get (HTTP inside) and
	// RealStore.Get (database/sql). The mock filter must skip MockStore,
	// preventing a false HTTP detection, while RealStore's POSTGRES call
	// is detected normally.
	out := pipeline(t, "./testdata/mockfilter", "HandleMock", nil, analysis.AlgoCHA)

	seen := make(map[trawl.ServiceType]bool, len(out.ExternalCalls))
	for _, ec := range out.ExternalCalls {
		seen[ec.ServiceType] = true
	}
	if !seen[trawl.ServiceTypePostgres] {
		t.Errorf("POSTGRES not detected via RealStore; saw %v", seen)
	}
	if seen[trawl.ServiceTypeHTTP] {
		t.Errorf("HTTP detected via MockStore — mock filter failed; saw %v", seen)
	}
}

func TestIntegration_NoMockNamesInOutput(t *testing.T) {
	t.Parallel()

	// Run the mock filter fixture under CHA. Any mock-inferred results must
	// use interface method labels, not concrete mock type names like
	// "(*MockStore).Get" or "MockStore".
	out := pipeline(t, "./testdata/mockfilter", "HandleMock", nil, analysis.AlgoCHA)

	for _, ec := range out.ExternalCalls {
		if strings.Contains(ec.Function, "MockStore") {
			t.Errorf("Function field contains mock type name: %q", ec.Function)
		}
		for _, link := range ec.CallChain {
			if strings.Contains(link, "MockStore") {
				t.Errorf("CallChain element contains mock type name: %q", link)
			}
		}
	}
}

func TestIntegration_CHA_GenericInterface_DirectDetection(t *testing.T) {
	t.Parallel()

	out := pipeline(t, "./testdata/generic", "HandleGeneric", nil, analysis.AlgoCHA)

	if len(out.ExternalCalls) == 0 {
		t.Fatal("pipeline returned no external calls; expected at least one POSTGRES call")
	}

	var foundPostgres bool
	for _, ec := range out.ExternalCalls {
		if ec.ServiceType == trawl.ServiceTypePostgres {
			foundPostgres = true
			if ec.ResolvedVia == trawl.ResolvedViaMockInference {
				t.Errorf("POSTGRES call resolved_via = %q, want %q or %q",
					ec.ResolvedVia, trawl.ResolvedViaDirect, trawl.ResolvedViaCrossModuleInference)
			}
		}
		// No call should use mock type names.
		if strings.Contains(ec.Function, "MockCache") {
			t.Errorf("Function field contains mock type name: %q", ec.Function)
		}
	}
	if !foundPostgres {
		t.Errorf("no POSTGRES call found in output: %v", out.ExternalCalls)
	}
}

func TestIntegration_CrossModule_ScopedWithoutDeps_MergesInterfaceAndConcrete(t *testing.T) {
	t.Parallel()
	// With lib/store loaded as an initial package and no dependency bodies,
	// CHA resolves Store.Get to both (*sqlStore).Get (import-inferred, low)
	// and (*MockStore).Get (mock-inferred, medium) at one call site.
	out := pipelineOpts(t, analysis.Options{
		Dir: crossmoduleSvcDir(t), Pattern: ".", Algo: analysis.AlgoCHA,
		Scope: []string{"example.com/lib/store"}, DependencyPolicy: analysis.DependencyNone,
	}, "HandleGet", nil)
	if len(out.ExternalCalls) != 1 {
		t.Fatalf("external calls = %d, want 1 merged record: %+v", len(out.ExternalCalls), out.ExternalCalls)
	}
	got := out.ExternalCalls[0]
	if got.Function != "example.com/lib/store.Store.Get" || got.Confidence != trawl.ConfidenceMedium || got.ResolvedVia != trawl.ResolvedViaMockInference {
		t.Errorf("merged record = %+v, want interface label, medium, mock_inference", got)
	}
}

func TestIntegration_CrossModule_CHA_OneRecordForSourceCall(t *testing.T) {
	t.Parallel()
	out := pipelineInDir(t, crossmoduleSvcDir(t), ".", "HandleGet", nil, analysis.AlgoCHA)
	if len(out.ExternalCalls) != 1 {
		t.Fatalf("external calls = %d, want 1: %+v", len(out.ExternalCalls), out.ExternalCalls)
	}
	got := out.ExternalCalls[0]
	if got.ResolvedVia != trawl.ResolvedViaCrossModuleTrace || got.ServiceType != trawl.ServiceTypePostgres {
		t.Errorf("record = %+v, want cross_module_trace/POSTGRES", got)
	}
	if !strings.HasSuffix(got.File, "svc.go") || got.ShortFunction != "(*sqlStore).Get" {
		t.Errorf("file/short_function = %q/%q, want svc.go/(*sqlStore).Get", got.File, got.ShortFunction)
	}
	for _, name := range append([]string{got.Function}, got.CallChain...) {
		if strings.Contains(name, "MockStore") {
			t.Errorf("mock type name in output: %q", name)
		}
	}
}

func TestIntegration_CrossModule_VTA_MockOnlySiteFallsBackToCHA(t *testing.T) {
	t.Parallel()
	// VTA resolves MockedHandler.Store.Get only to the mock (set by
	// NewMockedHandler). The mock edge must not count as resolved, so the CHA
	// edge to (*sqlStore).Get is added and traced into the dependency.
	out := pipelineInDir(t, crossmoduleSvcDir(t), ".", "HandleMockedGet", nil, analysis.AlgoVTA)
	if len(out.ExternalCalls) != 1 {
		t.Fatalf("external calls = %d, want 1: %+v", len(out.ExternalCalls), out.ExternalCalls)
	}
	got := out.ExternalCalls[0]
	if got.ResolvedVia != trawl.ResolvedViaCrossModuleTrace || got.ServiceType != trawl.ServiceTypePostgres {
		t.Errorf("record = %+v, want cross_module_trace/POSTGRES", got)
	}
}

func TestIntegration_DotlessModule_VTA_FallsBackToCHA(t *testing.T) {
	t.Parallel()
	// The module path "dotless" has no dot, like stdlib paths. Its Store
	// interface must still get the CHA fallback, so the call reaches impl.SQL.
	dir := filepath.Join(moduleRoot(t), "testdata", "dotless")
	out := pipelineInDir(t, dir, ".", "Handle", nil, analysis.AlgoVTA)
	if len(out.ExternalCalls) != 1 {
		t.Fatalf("external calls = %d, want 1: %+v", len(out.ExternalCalls), out.ExternalCalls)
	}
	got := out.ExternalCalls[0]
	if got.ServiceType != trawl.ServiceTypePostgres || got.ResolvedVia != trawl.ResolvedViaDirect {
		t.Errorf("record = %+v, want POSTGRES/direct", got)
	}
}
