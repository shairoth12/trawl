package analysis_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/analysis"
)

func TestSelectDependencyPkgs(t *testing.T) {
	// Not parallel: packages.Load shells out to the go toolchain.
	pkgs, err := analysis.LoadPackages(t.Context(), crossmoduleSvcDir(t), []string{"."})
	if err != nil {
		t.Fatalf("LoadPackages: %v", err)
	}
	paths := func(sel []*packages.Package) []string {
		out := make([]string, len(sel))
		for i, p := range sel {
			out[i] = p.PkgPath
		}
		return out
	}
	stdlib := analysis.StdlibPkgs(pkgs)
	modulePkgs, external := analysis.SelectDependencyPkgs(pkgs, "example.com/svc", nil, stdlib)

	t.Run("no_same_module_dependencies_in_fixture", func(t *testing.T) {
		if len(modulePkgs) != 0 {
			t.Errorf("modulePkgs = %v, want none", paths(modulePkgs))
		}
	})
	t.Run("selects_implementors_by_round_then_path", func(t *testing.T) {
		// Round 1: cache and store implement Store (invoked by svc).
		// Round 2: search implements Searcher (invoked inside store).
		want := []string{"example.com/lib/cache", "example.com/lib/store", "example.com/lib/search"}
		if diff := cmp.Diff(want, paths(external)); diff != "" {
			t.Errorf("selection mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("mock_only_package_not_selected", func(t *testing.T) {
		if slices.Contains(paths(external), "example.com/lib/storemock") {
			t.Errorf("storemock selected; mock implementors must be ignored")
		}
	})
	t.Run("indicator_packages_excluded", func(t *testing.T) {
		isInd := func(p string) bool { return p == "example.com/lib/search" }
		_, got := analysis.SelectDependencyPkgs(pkgs, "example.com/svc", isInd, stdlib)
		if slices.Contains(paths(got), "example.com/lib/search") {
			t.Errorf("search selected despite matching an indicator; got %v", paths(got))
		}
	})
	t.Run("stdlib_never_selected", func(t *testing.T) {
		for _, p := range paths(external) {
			if trawl.IsStandardLibrary(p) {
				t.Errorf("stdlib package %q selected", p)
			}
		}
	})
}

func TestRecoverBuild(t *testing.T) {
	t.Parallel()
	err := analysis.RecoverBuild(func() { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "--deps none") {
		t.Errorf("RecoverBuild(panic) = %v, want error mentioning --deps none", err)
	}
	if err := analysis.RecoverBuild(func() {}); err != nil {
		t.Errorf("RecoverBuild(no-op) = %v, want nil", err)
	}
}

func TestBuildProgram_StopsWhenCancelled(t *testing.T) {
	// Not parallel: packages.Load shells out to the go toolchain.
	pkgs, err := analysis.LoadPackages(t.Context(), moduleRoot(t), []string{"./testdata/basic"})
	if err != nil {
		t.Fatalf("LoadPackages: %v", err)
	}
	prog, ssaPkgs := ssautil.AllPackages(pkgs, 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := analysis.BuildProgram(ctx, prog); !errors.Is(err, context.Canceled) {
		t.Errorf("BuildProgram(cancelled ctx) = %v, want context.Canceled", err)
	}
	if fn := ssaPkgs[0].Func("HandleRequest"); fn == nil || len(fn.Blocks) != 0 {
		t.Errorf("HandleRequest was built after cancellation; want no package builds scheduled")
	}
}

func TestLoad_SameModulePackagesGetBodies(t *testing.T) {
	// Not parallel: analysis.Load shells out to the go toolchain.
	// cmd/trawl imports the module's internal packages, which are not initial
	// packages; DependencyAuto must build their bodies without --scope.
	r, err := analysis.Load(t.Context(), analysis.Options{Dir: moduleRoot(t), Pattern: "./cmd/trawl", Algo: analysis.AlgoCHA})
	if err != nil {
		t.Fatalf("Load(./cmd/trawl): %v", err)
	}
	const walkerPkg = "github.com/shairoth12/trawl/internal/walker"
	i := slices.Index(r.DependencyPkgs, walkerPkg)
	if i < 0 {
		t.Fatalf("DependencyPkgs = %v, want it to contain %q", r.DependencyPkgs, walkerPkg)
	}
	for _, p := range r.DependencyPkgs[:i] {
		if !strings.HasPrefix(p, r.Module) {
			t.Errorf("external package %q listed before same-module package %q", p, walkerPkg)
		}
	}
	fn := findFunc(r.Prog, "(*"+walkerPkg+".Walker).Walk")
	if fn == nil || len(fn.Blocks) == 0 {
		t.Errorf("(*Walker).Walk missing or body-less; same-module dependency bodies were not built")
	}
}

func TestCollectInvokedInterfaces_MethodExpression(t *testing.T) {
	t.Parallel()
	// I is called only through the method expression I.M, which go/types
	// records as a MethodExpr selection, not a MethodVal.
	const src = `package x

type I interface{ M() }

func f(i I) { I.M(i) }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatalf("parser.ParseFile: %v", err)
	}
	info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
	pkg, err := new(types.Config).Check("example.com/x", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("types.Check: %v", err)
	}

	got := analysis.CollectInvokedInterfaces([]*packages.Package{{PkgPath: pkg.Path(), Types: pkg, TypesInfo: info}}, nil)

	var names []string
	for _, named := range got["M"] {
		names = append(names, named.Obj().Pkg().Path()+"."+named.Obj().Name())
	}
	if diff := cmp.Diff([]string{"example.com/x.I"}, names); diff != "" {
		t.Errorf("CollectInvokedInterfaces(...)[\"M\"] mismatch (-want +got):\n%s", diff)
	}
}
