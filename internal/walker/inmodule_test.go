package walker_test

import (
	"testing"

	"github.com/shairoth12/trawl/internal/walker"
)

func TestInModule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		module, pkgPath string
		want            bool
	}{
		{"example.com/app", "example.com/app", true},
		{"example.com/app", "example.com/app/store", true},
		{"example.com/app", "example.com/app-extra", false},
		{"example.com/app", "example.com/appstore/x", false},
		{"example.com/app", "example.com/other", false},
		{"", "example.com/anything", true},
	}
	for _, tt := range tests {
		if got := walker.InModule(tt.module, tt.pkgPath); got != tt.want {
			t.Errorf("InModule(%q, %q) = %v, want %v", tt.module, tt.pkgPath, got, tt.want)
		}
	}
}
