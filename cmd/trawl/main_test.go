package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
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

	built := runtime.Version() // e.g. "go1.25.0"

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

// testdataPath returns the path to the testdata directory from the module root.
// Uses runtime.Caller so it works regardless of the test working directory.
func testdataPath(subdir string) string {
	_, file, _, _ := runtime.Caller(0)
	// file: .../cmd/trawl/main_test.go → cmd/trawl/ → cmd/ → module root
	moduleRoot := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	return filepath.Join(moduleRoot, "testdata", subdir)
}

func TestRun_Stats(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	var buf bytes.Buffer
	err := run([]string{
		"--pkg", testdataPath("basic"),
		"--entry", "HandleRequest",
		"--stats",
		"--log-level", "off",
	}, &buf)
	if err != nil {
		t.Fatalf("run(--stats) error = %v, want nil", err)
	}

	var result trawl.Result
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if result.Stats == nil {
		t.Fatal("Result.Stats = nil, want non-nil when --stats is provided")
	}
	s := result.Stats
	if s.PackagesLoaded == 0 {
		t.Errorf("Stats.PackagesLoaded = 0, want > 0")
	}
	if s.CallGraphNodes == 0 {
		t.Errorf("Stats.CallGraphNodes = 0, want > 0")
	}
	if s.CallGraphEdges == 0 {
		t.Errorf("Stats.CallGraphEdges = 0, want > 0")
	}
	if s.NodesVisited == 0 {
		t.Errorf("Stats.NodesVisited = 0, want > 0")
	}
	if s.EdgesExamined == 0 {
		t.Errorf("Stats.EdgesExamined = 0, want > 0")
	}
	if s.LoadDurationMs < 0 {
		t.Errorf("Stats.LoadDurationMs = %d, want >= 0", s.LoadDurationMs)
	}
	if s.WalkDurationMs < 0 {
		t.Errorf("Stats.WalkDurationMs = %d, want >= 0", s.WalkDurationMs)
	}
}

func TestRun_NoStats_OmitsField(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	var buf bytes.Buffer
	err := run([]string{
		"--pkg", testdataPath("basic"),
		"--entry", "HandleRequest",
		"--log-level", "off",
	}, &buf)
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if _, ok := raw["stats"]; ok {
		t.Errorf("JSON output contains \"stats\" key without --stats flag, want omitted")
	}
}
