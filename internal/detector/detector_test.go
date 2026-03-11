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
