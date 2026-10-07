package trawl

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestResultJSONRoundTrip(t *testing.T) {
	want := Result{
		EntryPoint: "github.com/example/app.HandleRequest",
		Package:    "github.com/example/app",
		ExternalCalls: []ExternalCall{
			{
				ServiceType:    ServiceTypeRedis,
				ImportPath:     "github.com/your-org/infra/redis",
				Function:       "Get",
				File:           "handler.go",
				Line:           42,
				CallChain:      []string{"HandleRequest", "fetchData", "redis.Get"},
				ResolvedVia:    ResolvedViaDirect,
				Confidence:     ConfidenceHigh,
				ShortFunction:  "Get",
				ShortCallChain: []string{"HandleRequest", "fetchData", "redis.Get"},
			},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(Result) error: %v", err)
	}

	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(Result) error: %v", err)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Result JSON round-trip mismatch (-want +got):\n%s", diff)
	}
}

func TestExternalCallJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		call ExternalCall
	}{
		{
			name: "full fields",
			call: ExternalCall{
				ServiceType: ServiceTypePubSub,
				ImportPath:  "github.com/your-org/infra/pubsub",
				Function:    "Publish",
				File:        "events.go",
				Line:        100,
				CallChain:   []string{"SendEvent", "pubsub.Publish"},
				ResolvedVia: ResolvedViaDirect,
				Confidence:  ConfidenceHigh,
			},
		},
		{
			name: "mock_inference",
			call: ExternalCall{
				ServiceType: ServiceTypeRedis,
				ImportPath:  "github.com/example/cache",
				Function:    "cache.ICache.Get",
				File:        "handler.go",
				Line:        30,
				CallChain:   []string{"Handle", "cache.ICache.Get"},
				ResolvedVia: ResolvedViaMockInference,
				Confidence:  ConfidenceMedium,
			},
		},
		{
			name: "cross_module_inference",
			call: ExternalCall{
				ServiceType: ServiceTypeRedis,
				ImportPath:  "github.com/example/rediscache",
				Function:    "rediscache.Cache.Get",
				File:        "handler.go",
				Line:        50,
				CallChain:   []string{"Handle", "rediscache.Cache.Get"},
				ResolvedVia: ResolvedViaCrossModuleInference,
				Confidence:  ConfidenceLow,
			},
		},
		{
			name: "nil call chain",
			call: ExternalCall{
				ServiceType: ServiceTypeHTTP,
				ImportPath:  "net/http",
				Function:    "Get",
				File:        "client.go",
				Line:        55,
				CallChain:   nil,
			},
		},
		{
			name: "empty call chain",
			call: ExternalCall{
				ServiceType: ServiceTypeGRPC,
				ImportPath:  "google.golang.org/grpc",
				Function:    "Dial",
				File:        "conn.go",
				Line:        10,
				CallChain:   []string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.call)
			if err != nil {
				t.Fatalf("json.Marshal(ExternalCall) error: %v", err)
			}

			var got ExternalCall
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("json.Unmarshal(ExternalCall) error: %v", err)
			}

			if diff := cmp.Diff(tt.call, got); diff != "" {
				t.Errorf("ExternalCall JSON round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResultWithStats_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := Result{
		EntryPoint:    "github.com/example/app.HandleRequest",
		Package:       "github.com/example/app",
		ExternalCalls: []ExternalCall{},
		Stats: &AnalysisStats{
			PackagesLoaded:     12,
			PackagesAnalyzed:   4,
			DependencyPackages: 3,
			CallGraphNodes:     300,
			CallGraphEdges:     850,
			NodesVisited:       45,
			EdgesExamined:      120,
			UnresolvedInvokes:  2,
			LoadDurationMs:     1500,
			WalkDurationMs:     30,
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(Result with Stats) error: %v", err)
	}

	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(Result with Stats) error: %v", err)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Result with Stats JSON round-trip mismatch (-want +got):\n%s", diff)
	}

	var raw struct {
		Stats map[string]json.RawMessage `json:"stats"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal to raw map error: %v", err)
	}
	for _, key := range []string{"packages_analyzed", "dependency_packages", "unresolved_invokes"} {
		if _, ok := raw.Stats[key]; !ok {
			t.Errorf("stats JSON missing key %q", key)
		}
	}
}

func TestResultWithoutStats_OmitsStatsKey(t *testing.T) {
	t.Parallel()

	r := Result{
		EntryPoint:    "github.com/example/app.HandleRequest",
		Package:       "github.com/example/app",
		ExternalCalls: []ExternalCall{},
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal(Result without Stats) error: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal to raw map error: %v", err)
	}

	if _, ok := raw["stats"]; ok {
		t.Errorf("JSON output contains %q key, want omitted when Stats is nil", "stats")
	}
}
