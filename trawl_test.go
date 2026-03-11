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

func TestResultJSONRoundTrip(t *testing.T) {
	want := Result{
		EntryPoint: "github.com/example/app.HandleRequest",
		Package:    "github.com/example/app",
		ExternalCalls: []ExternalCall{
			{
				ServiceType: ServiceTypeRedis,
				ImportPath:  "github.com/your-org/infra/redis",
				Function:    "Get",
				File:        "handler.go",
				Line:        42,
				CallChain:   []string{"HandleRequest", "fetchData", "redis.Get"},
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
		tmp.Close()

		_, err = LoadConfig(context.Background(), tmp.Name())
		if err == nil {
			t.Fatalf("LoadConfig(invalid.yaml) = nil error, want parse error")
		}
	})
}
