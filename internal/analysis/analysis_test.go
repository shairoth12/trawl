package analysis_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/shairoth12/trawl/internal/analysis"
)

// moduleRoot returns the module root directory by locating the source file
// via runtime.Caller. This is robust regardless of the working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	// file: .../internal/analysis/analysis_test.go
	// Three Dir calls: analysis/ → internal/ → module root
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// brokenModuleDir creates a temporary module containing a Go file with a
// type error and returns the directory path.
func brokenModuleDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	gomod := "module example.com/broken\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}

	brokenGo := "package broken\n\nfunc oops() { _ = undeclaredVar }\n"
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte(brokenGo), 0o644); err != nil {
		t.Fatalf("WriteFile(broken.go): %v", err)
	}

	return dir
}

func TestLoad(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	brokenDir := brokenModuleDir(t)

	tests := []struct {
		name            string
		dir             string
		pattern         string
		algo            analysis.Algo
		wantErr         bool
		wantErrSentinel error
		check           func(t *testing.T, r *analysis.LoadResult)
	}{
		{
			name:    "VTA_basic",
			dir:     root,
			pattern: "./testdata/basic",
			algo:    analysis.AlgoVTA,
			check: func(t *testing.T, r *analysis.LoadResult) {
				t.Helper()
				if r.SSAPkg == nil {
					t.Errorf("SSAPkg = nil, want non-nil")
				}
				if r.Graph == nil {
					t.Errorf("Graph = nil, want non-nil")
				}
				if r.Module != "github.com/shairoth12/trawl" {
					t.Errorf("Module = %q, want %q", r.Module, "github.com/shairoth12/trawl")
				}
				if r.Graph != nil && len(r.Graph.Nodes) == 0 {
					t.Errorf("Graph.Nodes is empty, want non-empty")
				}
			},
		},
		{
			name:    "RTA_nil_graph",
			dir:     root,
			pattern: "./testdata/basic",
			algo:    analysis.AlgoRTA,
			check: func(t *testing.T, r *analysis.LoadResult) {
				t.Helper()
				if r.Graph != nil {
					t.Errorf("Graph = %v, want nil (RTA graph built by caller)", r.Graph)
				}
				if r.SSAPkg == nil {
					t.Errorf("SSAPkg = nil, want non-nil")
				}
			},
		},
		{
			name:    "nonexistent_path",
			dir:     root,
			pattern: "./nonexistent",
			algo:    analysis.AlgoVTA,
			wantErr: true,
		},
		{
			name:            "broken_package",
			dir:             brokenDir,
			pattern:         ".",
			algo:            analysis.AlgoVTA,
			wantErr:         true,
			wantErrSentinel: analysis.ErrPackageLoad,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := analysis.Load(t.Context(), tc.dir, tc.pattern, tc.algo)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load(...) = nil error, want an error")
				}
				if tc.wantErrSentinel != nil && !errors.Is(err, tc.wantErrSentinel) {
					t.Errorf("Load(...) error = %v, want errors.Is(err, %v)", err, tc.wantErrSentinel)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load(...) returned unexpected error: %v", err)
			}
			if result == nil {
				t.Fatal("Load(...) = nil, want non-nil")
			}
			if tc.check != nil {
				tc.check(t, result)
			}
		})
	}
}
