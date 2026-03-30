package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/shairoth12/trawl"
)

func TestVersionInfo(t *testing.T) {
	t.Parallel()

	got := versionInfo()

	if !strings.HasPrefix(got, "trawl ") {
		t.Errorf("versionInfo() = %q, want prefix %q", got, "trawl ")
	}
	if !strings.Contains(got, runtime.Version()) {
		t.Errorf("versionInfo() = %q, want to contain runtime version %q", got, runtime.Version())
	}
}

func TestToolchainWarning(t *testing.T) {
	t.Parallel()

	built := runtime.Version() // e.g. "go1.24.0"

	tests := []struct {
		name          string
		hostGoVersion string
		wantWarning   bool
	}{
		{
			name:          "matching versions",
			hostGoVersion: built,
			wantWarning:   false,
		},
		{
			name:          "empty host version",
			hostGoVersion: "",
			wantWarning:   false,
		},
		{
			name:          "newer host toolchain",
			hostGoVersion: "go99.0.0",
			wantWarning:   true,
		},
		{
			name:          "older host toolchain",
			hostGoVersion: "go1.0.0",
			wantWarning:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toolchainWarning(tt.hostGoVersion)
			hasWarning := got != ""

			if hasWarning != tt.wantWarning {
				t.Errorf("toolchainWarning(%q) returned warning=%v, want warning=%v (output: %q)",
					tt.hostGoVersion, hasWarning, tt.wantWarning, got)
			}
			if tt.wantWarning {
				if !strings.Contains(got, built) {
					t.Errorf("toolchainWarning(%q) = %q, want to contain built version %q",
						tt.hostGoVersion, got, built)
				}
				if !strings.Contains(got, tt.hostGoVersion) {
					t.Errorf("toolchainWarning(%q) = %q, want to contain host version",
						tt.hostGoVersion, got)
				}
			}
		})
	}
}

func TestRun_Version(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := run([]string{"--version"}, &buf); err != nil {
		t.Fatalf("run(--version) error = %v, want nil", err)
	}

	got := buf.String()
	if !strings.Contains(got, "trawl") {
		t.Errorf("run(--version) output = %q, want to contain %q", got, "trawl")
	}
	if !strings.Contains(got, runtime.Version()) {
		t.Errorf("run(--version) output = %q, want to contain Go version %q", got, runtime.Version())
	}
}

func TestRun_Help(t *testing.T) {
	t.Parallel()

	// --help must exit cleanly (no error) even though it prints to stderr.
	if err := run([]string{"--help"}, &bytes.Buffer{}); err != nil {
		t.Errorf("run(--help) error = %v, want nil", err)
	}
}

func TestDeduplicateCalls(t *testing.T) {
	t.Parallel()

	calls := []trawl.ExternalCall{
		{
			ServiceType: "HTTP",
			ImportPath:  "net/http",
			Function:    "Get",
			CallChain:   []string{"A", "B", "C"},
			ResolvedVia: trawl.ResolvedViaDirect,
			Confidence:  trawl.ConfidenceHigh,
		},
		{
			ServiceType: "HTTP",
			ImportPath:  "net/http",
			Function:    "Get",
			CallChain:   []string{"A", "D"},
			ResolvedVia: trawl.ResolvedViaDirect,
			Confidence:  trawl.ConfidenceHigh,
		},
		{
			ServiceType: "REDIS",
			ImportPath:  "github.com/redis/go-redis",
			Function:    "Set",
			CallChain:   []string{"A", "E"},
			ResolvedVia: trawl.ResolvedViaDirect,
			Confidence:  trawl.ConfidenceHigh,
		},
	}

	got := deduplicateCalls(calls)

	if len(got) != 2 {
		t.Fatalf("deduplicateCalls() len = %d, want 2", len(got))
	}

	// HTTP entry should keep the shorter call chain ["A", "D"].
	if got[0].ServiceType != "HTTP" {
		t.Errorf("got[0].ServiceType = %q, want %q", got[0].ServiceType, "HTTP")
	}
	if len(got[0].CallChain) != 2 {
		t.Errorf("got[0].CallChain len = %d, want 2 (shortest chain)", len(got[0].CallChain))
	}

	// REDIS entry unchanged.
	if got[1].ServiceType != "REDIS" {
		t.Errorf("got[1].ServiceType = %q, want %q", got[1].ServiceType, "REDIS")
	}
}

func TestDeduplicateCalls_NoDuplicates(t *testing.T) {
	t.Parallel()

	calls := []trawl.ExternalCall{
		{ServiceType: "HTTP", ImportPath: "net/http", Function: "Get", CallChain: []string{"A"}},
		{ServiceType: "REDIS", ImportPath: "go-redis", Function: "Set", CallChain: []string{"B"}},
	}

	got := deduplicateCalls(calls)

	if len(got) != 2 {
		t.Fatalf("deduplicateCalls() len = %d, want 2", len(got))
	}
}

func TestDeduplicateCalls_Empty(t *testing.T) {
	t.Parallel()

	got := deduplicateCalls(nil)

	if got != nil {
		t.Errorf("deduplicateCalls(nil) = %v, want nil", got)
	}
}
