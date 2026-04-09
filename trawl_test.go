package trawl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestShortenName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "generic_with_nested_paths",
			input: "github.com/foo/rediscache.IRedisCache[*github.com/foo/bar.CacheItem, github.com/foo/baz.Cache].Set",
			want:  "IRedisCache.Set",
		},
		{
			name:  "pointer_receiver_with_path",
			input: "(*github.com/foo/pkg.TypeName).Method",
			want:  "(*TypeName).Method",
		},
		{
			name:  "cloud_style_path",
			input: "cloud.google.com/go/datastore.NameKey",
			want:  "NameKey",
		},
		{
			name:  "interface_method_with_path",
			input: "github.com/foo/msgraph.Authenticator.GetTokenRoles",
			want:  "Authenticator.GetTokenRoles",
		},
		{
			name:  "stdlib_with_slash",
			input: "net/http.Get",
			want:  "Get",
		},
		{
			name:  "no_path_no_generics",
			input: "HandleRequest",
			want:  "HandleRequest",
		},
		{
			name:  "already_short_pointer_receiver",
			input: "(*userDetails).GetUserDetails",
			want:  "(*userDetails).GetUserDetails",
		},
		{
			name:  "generic_single_param",
			input: "github.com/foo/msgraph.IBatcher[encoding/json.RawMessage].SendBatchRawResponse",
			want:  "IBatcher.SendBatchRawResponse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ShortenName(tt.input)
			if got != tt.want {
				t.Errorf("ShortenName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

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
			PackagesLoaded: 12,
			CallGraphNodes: 300,
			CallGraphEdges: 850,
			NodesVisited:   45,
			EdgesExamined:  120,
			LoadDurationMs: 1500,
			WalkDurationMs: 30,
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
}

func TestResultWithoutStats_OmitsStatsKey(t *testing.T) {
	t.Parallel()

	r := NewResult("github.com/example/app.HandleRequest", "github.com/example/app")

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

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "empty config is valid",
			cfg:     Config{},
			wantErr: false,
		},
		{
			name: "valid indicators",
			cfg: Config{Indicators: []Indicator{
				{Package: "github.com/foo/bar", ServiceType: ServiceTypeRedis},
				{Package: "database/sql", ServiceType: ServiceTypePostgres},
			}},
			wantErr: false,
		},
		{
			name: "valid indicator with wrapper_for",
			cfg: Config{Indicators: []Indicator{
				{
					Package:     "github.com/foo/cache",
					ServiceType: ServiceTypeRedis,
					WrapperFor:  []string{"github.com/go-redis/redis"},
				},
			}},
			wantErr: false,
		},
		{
			name: "empty package",
			cfg: Config{Indicators: []Indicator{
				{Package: "", ServiceType: ServiceTypeHTTP},
			}},
			wantErr: true,
		},
		{
			name: "empty service_type",
			cfg: Config{Indicators: []Indicator{
				{Package: "github.com/foo/bar", ServiceType: ""},
			}},
			wantErr: true,
		},
		{
			name: "empty wrapper_for entry",
			cfg: Config{Indicators: []Indicator{
				{
					Package:     "github.com/foo/cache",
					ServiceType: ServiceTypeRedis,
					WrapperFor:  []string{"github.com/go-redis/redis", ""},
				},
			}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("empty path returns zero Config", func(t *testing.T) {
		cfg, err := LoadConfig(context.Background(), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Indicators) != 0 {
			t.Errorf("LoadConfig(%q) = %d indicators, want 0", "", len(cfg.Indicators))
		}
	})

	t.Run("valid YAML file returns correct indicators", func(t *testing.T) {
		cfg, err := LoadConfig(context.Background(), filepath.Join("testdata", "config", "valid.yaml"))
		if err != nil {
			t.Fatalf("LoadConfig(valid.yaml) error: %v", err)
		}
		if len(cfg.Indicators) != 2 {
			t.Fatalf("LoadConfig(valid.yaml) = %d indicators, want 2", len(cfg.Indicators))
		}

		want := []Indicator{
			{Package: "github.com/your-org/infra/redis", ServiceType: ServiceTypeRedis},
			{Package: "github.com/your-org/infra/pubsub", ServiceType: ServiceTypePubSub},
		}
		if diff := cmp.Diff(want, cfg.Indicators); diff != "" {
			t.Errorf("LoadConfig(valid.yaml) indicators mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		_, err := LoadConfig(context.Background(), filepath.Join("testdata", "config", "nonexistent.yaml"))
		if err == nil {
			t.Errorf("LoadConfig(nonexistent.yaml) = nil error, want error")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("LoadConfig(nonexistent.yaml) error = %v, want os.ErrNotExist in chain", err)
		}
	})

	t.Run("invalid YAML returns error", func(t *testing.T) {
		tmp, err := os.CreateTemp(t.TempDir(), "invalid-*.yaml")
		if err != nil {
			t.Fatalf("creating temp file: %v", err)
		}
		if _, err := tmp.WriteString("indicators: [\x00bad yaml"); err != nil {
			t.Fatalf("writing temp file: %v", err)
		}
		if err := tmp.Close(); err != nil {
			t.Fatalf("closing temp file: %v", err)
		}

		_, err = LoadConfig(context.Background(), tmp.Name())
		if err == nil {
			t.Fatalf("LoadConfig(invalid.yaml) = nil error, want parse error")
		}
	})

	t.Run("wrapper_for field parsed correctly", func(t *testing.T) {
		cfg, err := LoadConfig(context.Background(), filepath.Join("testdata", "config", "wrapper.yaml"))
		if err != nil {
			t.Fatalf("LoadConfig(wrapper.yaml) error: %v", err)
		}
		if len(cfg.Indicators) != 2 {
			t.Fatalf("LoadConfig(wrapper.yaml) = %d indicators, want 2", len(cfg.Indicators))
		}

		ind := cfg.Indicators[0]
		if ind.Package != "github.com/example/rediscache" {
			t.Errorf("Indicators[0].Package = %q, want %q", ind.Package, "github.com/example/rediscache")
		}
		if len(ind.WrapperFor) != 2 {
			t.Fatalf("Indicators[0].WrapperFor len = %d, want 2", len(ind.WrapperFor))
		}
		if ind.WrapperFor[0] != "github.com/custom-redis/client" {
			t.Errorf("Indicators[0].WrapperFor[0] = %q, want %q", ind.WrapperFor[0], "github.com/custom-redis/client")
		}
		if ind.WrapperFor[1] != "github.com/another-redis/lib" {
			t.Errorf("Indicators[0].WrapperFor[1] = %q, want %q", ind.WrapperFor[1], "github.com/another-redis/lib")
		}
	})
}
