package detector

import (
	"testing"

	"github.com/shairoth12/trawl"
)

func TestDetect(t *testing.T) {
	d := New(nil)

	tests := []struct {
		name       string
		importPath string
		want       trawl.ServiceType
		wantOK     bool
	}{
		{"stdlib_http_exact", "net/http", trawl.ServiceTypeHTTP, true},
		{"stdlib_http_subpkg", "net/http/httptest", trawl.ServiceTypeHTTP, true},
		{"grpc_subpkg", "google.golang.org/grpc/credentials", trawl.ServiceTypeGRPC, true},
		{"redis_versioned", "github.com/go-redis/redis/v9", trawl.ServiceTypeRedis, true},
		{"pgx_versioned", "github.com/jackc/pgx/v5", trawl.ServiceTypePostgres, true},
		{"no_match", "github.com/unrelated/pkg", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := d.Detect(tt.importPath)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Detect(%q) = (%q, %v), want (%q, %v)", tt.importPath, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDetect_UserOverridesBuiltin(t *testing.T) {
	user := []trawl.Indicator{
		{Package: "net/http", ServiceType: "CUSTOM"},
	}
	d := New(user)

	got, ok := d.Detect("net/http")
	if got != "CUSTOM" || !ok {
		t.Errorf("Detect(%q) = (%q, %v), want (%q, true)", "net/http", got, ok, trawl.ServiceType("CUSTOM"))
	}
}

func TestDetect_UserUnknownType(t *testing.T) {
	user := []trawl.Indicator{
		{Package: "github.com/myorg/bolt", ServiceType: "BOLT"},
	}
	d := New(user)

	got, ok := d.Detect("github.com/myorg/bolt/client")
	if got != "BOLT" || !ok {
		t.Errorf("Detect(%q) = (%q, %v), want (%q, true)", "github.com/myorg/bolt/client", got, ok, trawl.ServiceType("BOLT"))
	}
}

func TestDetect_WrapperFor(t *testing.T) {
	t.Parallel()

	user := []trawl.Indicator{{
		Package:     "github.com/example/rediscache",
		ServiceType: trawl.ServiceTypeRedis,
		WrapperFor:  []string{"github.com/custom-redis/client"},
	}}
	d := New(user)

	tests := []struct {
		name       string
		importPath string
		want       trawl.ServiceType
		wantOK     bool
	}{
		{"wrapper_matches", "github.com/example/rediscache/cache", trawl.ServiceTypeRedis, true},
		{"wrapped_lib_matches", "github.com/custom-redis/client/v2", trawl.ServiceTypeRedis, true},
		{"unrelated", "github.com/unrelated/pkg", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := d.Detect(tt.importPath)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Detect(%q) = (%q, %v), want (%q, %v)",
					tt.importPath, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDetect_SkipInternal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		importPath string
		want       trawl.ServiceType
		wantOK     bool
	}{
		// Builtin indicators have SkipInternal: true.
		{
			name:       "redis_direct_match",
			importPath: "github.com/redis/go-redis/v9",
			want:       trawl.ServiceTypeRedis,
			wantOK:     true,
		},
		{
			name:       "redis_public_subpkg",
			importPath: "github.com/redis/go-redis/v9/extra/redisotel",
			want:       trawl.ServiceTypeRedis,
			wantOK:     true,
		},
		{
			name:       "redis_internal_subpkg_skipped",
			importPath: "github.com/redis/go-redis/v9/internal/proto",
			want:       "",
			wantOK:     false,
		},
		{
			name:       "grpc_internal_skipped",
			importPath: "google.golang.org/grpc/internal/transport",
			want:       "",
			wantOK:     false,
		},
		{
			name:       "grpc_public_subpkg",
			importPath: "google.golang.org/grpc/credentials",
			want:       trawl.ServiceTypeGRPC,
			wantOK:     true,
		},
		// User indicator without SkipInternal: internal subpkgs still match.
		{
			name:       "user_indicator_no_skip",
			importPath: "github.com/myorg/svc/internal/proto",
			want:       trawl.ServiceType("MYSVC"),
			wantOK:     true,
		},
	}

	user := []trawl.Indicator{
		{Package: "github.com/myorg/svc", ServiceType: "MYSVC", SkipInternal: false},
	}
	d := New(user)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := d.Detect(tt.importPath)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Detect(%q) = (%q, %v), want (%q, %v)",
					tt.importPath, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
