package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shairoth12/trawl"
)

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     trawl.Config
		wantErr bool
	}{
		{
			name:    "empty config is valid",
			cfg:     trawl.Config{},
			wantErr: false,
		},
		{
			name: "valid indicators",
			cfg: trawl.Config{Indicators: []trawl.Indicator{
				{Package: "github.com/foo/bar", ServiceType: trawl.ServiceTypeRedis},
				{Package: "database/sql", ServiceType: trawl.ServiceTypePostgres},
			}},
			wantErr: false,
		},
		{
			name: "valid indicator with wrapper_for",
			cfg: trawl.Config{Indicators: []trawl.Indicator{
				{
					Package:     "github.com/foo/cache",
					ServiceType: trawl.ServiceTypeRedis,
					WrapperFor:  []string{"github.com/go-redis/redis"},
				},
			}},
			wantErr: false,
		},
		{
			name: "empty package",
			cfg: trawl.Config{Indicators: []trawl.Indicator{
				{Package: "", ServiceType: trawl.ServiceTypeHTTP},
			}},
			wantErr: true,
		},
		{
			name: "empty service_type",
			cfg: trawl.Config{Indicators: []trawl.Indicator{
				{Package: "github.com/foo/bar", ServiceType: ""},
			}},
			wantErr: true,
		},
		{
			name: "empty wrapper_for entry",
			cfg: trawl.Config{Indicators: []trawl.Indicator{
				{
					Package:     "github.com/foo/cache",
					ServiceType: trawl.ServiceTypeRedis,
					WrapperFor:  []string{"github.com/go-redis/redis", ""},
				},
			}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateConfig(tt.cfg)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Errorf("validateConfig() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("empty path returns zero Config", func(t *testing.T) {
		cfg, err := loadConfig("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Indicators) != 0 {
			t.Errorf("loadConfig(%q) = %d indicators, want 0", "", len(cfg.Indicators))
		}
	})

	t.Run("valid YAML file returns correct indicators", func(t *testing.T) {
		cfg, err := loadConfig(filepath.Join("testdata", "config", "valid.yaml"))
		if err != nil {
			t.Fatalf("loadConfig(valid.yaml) error: %v", err)
		}
		if len(cfg.Indicators) != 2 {
			t.Fatalf("loadConfig(valid.yaml) = %d indicators, want 2", len(cfg.Indicators))
		}

		want := []trawl.Indicator{
			{Package: "github.com/your-org/infra/redis", ServiceType: trawl.ServiceTypeRedis},
			{Package: "github.com/your-org/infra/pubsub", ServiceType: trawl.ServiceTypePubSub},
		}
		if diff := cmp.Diff(want, cfg.Indicators); diff != "" {
			t.Errorf("loadConfig(valid.yaml) indicators mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		_, err := loadConfig(filepath.Join("testdata", "config", "nonexistent.yaml"))
		if err == nil {
			t.Errorf("loadConfig(nonexistent.yaml) = nil error, want error")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("loadConfig(nonexistent.yaml) error = %v, want os.ErrNotExist in chain", err)
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

		_, err = loadConfig(tmp.Name())
		if err == nil {
			t.Fatalf("loadConfig(invalid.yaml) = nil error, want parse error")
		}
	})

	t.Run("wrapper_for field parsed correctly", func(t *testing.T) {
		cfg, err := loadConfig(filepath.Join("testdata", "config", "wrapper.yaml"))
		if err != nil {
			t.Fatalf("loadConfig(wrapper.yaml) error: %v", err)
		}
		if len(cfg.Indicators) != 2 {
			t.Fatalf("loadConfig(wrapper.yaml) = %d indicators, want 2", len(cfg.Indicators))
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
