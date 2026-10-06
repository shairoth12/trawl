package analysis_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/shairoth12/trawl/internal/analysis"
)

// hasEdge reports whether the call graph has a direct edge from → to.
func hasEdge(g *callgraph.Graph, from, to *ssa.Function) bool {
	n := g.Nodes[from]
	if n == nil {
		return false
	}
	for _, e := range n.Out {
		if e.Callee != nil && e.Callee.Func == to {
			return true
		}
	}
	return false
}

// crossmoduleSvcDir returns the root of the two-module fixture's service module.
func crossmoduleSvcDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "testdata", "crossmodule", "svc")
}

// findFunc returns the SSA function whose String() equals name, or nil.
func findFunc(prog *ssa.Program, name string) *ssa.Function {
	for fn := range ssautil.AllFunctions(prog) {
		if fn.String() == name {
			return fn
		}
	}
	return nil
}

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

	gomod := "module example.com/broken\n\ngo 1.25\n"
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
		opts            analysis.Options
		wantErr         bool
		wantErrSentinel error
		check           func(t *testing.T, r *analysis.LoadResult)
	}{
		{
			name: "VTA_basic",
			opts: analysis.Options{Dir: root, Pattern: "./testdata/basic", Algo: analysis.AlgoVTA},
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
			name: "RTA_nil_graph",
			opts: analysis.Options{Dir: root, Pattern: "./testdata/basic", Algo: analysis.AlgoRTA},
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
			opts:    analysis.Options{Dir: root, Pattern: "./nonexistent", Algo: analysis.AlgoVTA},
			wantErr: true,
		},
		{
			name:            "broken_package",
			opts:            analysis.Options{Dir: brokenDir, Pattern: ".", Algo: analysis.AlgoVTA},
			wantErr:         true,
			wantErrSentinel: analysis.ErrPackageLoad,
		},
		{
			name: "VTA_with_scope",
			opts: analysis.Options{Dir: root, Pattern: "./testdata/scope/leaf", Algo: analysis.AlgoVTA, Scope: []string{"./testdata/scope/..."}},
			check: func(t *testing.T, r *analysis.LoadResult) {
				t.Helper()
				if r.SSAPkg.Pkg.Name() != "leaf" {
					t.Errorf("SSAPkg.Pkg.Name() = %q, want %q", r.SSAPkg.Pkg.Name(), "leaf")
				}
				if r.Graph == nil {
					t.Errorf("Graph = nil, want non-nil")
				}
			},
		},
		{
			name: "CHA_with_scope",
			opts: analysis.Options{Dir: root, Pattern: "./testdata/scope/leaf", Algo: analysis.AlgoCHA, Scope: []string{"./testdata/scope/..."}},
			check: func(t *testing.T, r *analysis.LoadResult) {
				t.Helper()
				if r.SSAPkg.Pkg.Name() != "leaf" {
					t.Errorf("SSAPkg.Pkg.Name() = %q, want %q", r.SSAPkg.Pkg.Name(), "leaf")
				}
				if r.Graph == nil {
					t.Errorf("Graph = nil, want non-nil")
				}
			},
		},
		{
			name: "crossmodule_DepNone_no_dependency_bodies",
			opts: analysis.Options{Dir: crossmoduleSvcDir(t), Pattern: ".", Algo: analysis.AlgoCHA, DependencyPolicy: analysis.DependencyNone},
			check: func(t *testing.T, r *analysis.LoadResult) {
				t.Helper()
				if r.Module != "example.com/svc" {
					t.Errorf("Module = %q, want %q", r.Module, "example.com/svc")
				}
				if len(r.DependencyPkgs) != 0 {
					t.Errorf("DependencyPkgs = %v, want none under DependencyNone", r.DependencyPkgs)
				}
				if fn := findFunc(r.Prog, "(*example.com/lib/store.sqlStore).Get"); fn != nil && len(fn.Blocks) != 0 {
					t.Errorf("(*sqlStore).Get has %d blocks under DependencyNone, want 0", len(fn.Blocks))
				}
			},
		},
		{
			name: "crossmodule_DepAuto_builds_dependency_bodies",
			opts: analysis.Options{Dir: crossmoduleSvcDir(t), Pattern: ".", Algo: analysis.AlgoCHA},
			check: func(t *testing.T, r *analysis.LoadResult) {
				t.Helper()
				want := []string{"example.com/lib/cache", "example.com/lib/store", "example.com/lib/search"}
				if diff := cmp.Diff(want, r.DependencyPkgs); diff != "" {
					t.Errorf("DependencyPkgs (-want +got):\n%s", diff)
				}
				if r.PackagesAnalyzed != 1+len(want) {
					t.Errorf("PackagesAnalyzed = %d, want %d", r.PackagesAnalyzed, 1+len(want))
				}
				get := findFunc(r.Prog, "(*example.com/lib/store.sqlStore).Get")
				if get == nil || len(get.Blocks) == 0 {
					t.Fatalf("(*sqlStore).Get missing or body-less under DependencyAuto")
				}
				entry := findFunc(r.Prog, "(*example.com/svc.Handler).HandleGet")
				if !hasEdge(r.Graph, entry, get) {
					t.Errorf("no call-graph edge HandleGet → (*sqlStore).Get")
				}
			},
		},
		{
			name: "cap_keeps_earlier_rounds",
			opts: analysis.Options{Dir: crossmoduleSvcDir(t), Pattern: ".", Algo: analysis.AlgoCHA, MaxDependencyPkgs: 2},
			check: func(t *testing.T, r *analysis.LoadResult) {
				t.Helper()
				if diff := cmp.Diff([]string{"example.com/lib/cache", "example.com/lib/store"}, r.DependencyPkgs); diff != "" {
					t.Errorf("DependencyPkgs (-want +got):\n%s", diff)
				}
				if r.DependencyPkgsSkipped != 1 {
					t.Errorf("DependencyPkgsSkipped = %d, want 1", r.DependencyPkgsSkipped)
				}
			},
		},
		{
			name:    "invalid_deps_policy",
			opts:    analysis.Options{Dir: root, Pattern: "./testdata/basic", Algo: analysis.AlgoVTA, DependencyPolicy: analysis.DependencyPolicy("bogus")},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := analysis.Load(t.Context(), tc.opts)

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

func TestLoad_VTA_StdlibInvokesStayEmpty(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// No value reaches w (http.ResponseWriter) or resp.Body (io.ReadCloser)
	// under VTA. CHA would match every implementation in the program, so the
	// empty-invoke fill must skip interfaces declared in the standard library.
	r, err := analysis.Load(t.Context(), analysis.Options{Dir: moduleRoot(t), Pattern: "./testdata/chain", Algo: analysis.AlgoVTA})
	if err != nil {
		t.Fatalf("analysis.Load(chain): %v", err)
	}
	fn := r.SSAPkg.Func("HandleChain")
	if fn == nil {
		t.Fatal("HandleChain not found in chain fixture")
	}
	for _, e := range r.Graph.Nodes[fn].Out {
		if e.Site != nil && e.Site.Common().IsInvoke() {
			t.Errorf("HandleChain has edge %s → %s on a stdlib interface call, want none", e.Site, e.Callee.Func)
		}
	}
}
