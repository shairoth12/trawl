// Package trawl_test contains end-to-end integration tests for the full
// analysis pipeline: load → resolve → walk → JSON result.
package trawl_test

import (
	"encoding/json"
	"path/filepath"
	"runtime"
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

// pipeline runs the full analysis pipeline for a fixture and returns the
// trawl.Result. It fails the test immediately on any internal error.
func pipeline(
	t *testing.T,
	pattern, entryName string,
	indicators []trawl.Indicator,
	algo analysis.Algo,
	scope ...string,
) trawl.Result {
	t.Helper()
	root := moduleRoot(t)

	loadResult, err := analysis.Load(t.Context(), root, pattern, algo, scope...)
	if err != nil {
		t.Fatalf("analysis.Load(%q): %v", pattern, err)
	}

	fn, err := analysis.Resolve(loadResult, entryName)
	if err != nil {
		t.Fatalf("analysis.Resolve(%q): %v", entryName, err)
	}

	graph := loadResult.Graph
	if algo == analysis.AlgoRTA {
		rtaResult := rta.Analyze([]*ssa.Function{fn}, true)
		graph = rtaResult.CallGraph
	}

	det := detector.New(indicators)
	w := walker.New(graph, det, loadResult.Module, loadResult.Prog.Fset)
	calls, err := w.Walk(fn)
	if err != nil {
		t.Fatalf("Walk(%q): %v", entryName, err)
	}

	out := trawl.NewResult(fn.String(), loadResult.SSAPkg.Pkg.Path())
	out.ExternalCalls = calls
	return out
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
