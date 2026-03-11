package analysis_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shairoth12/trawl/internal/analysis"
)

// moduleRoot returns the module root directory (two levels up from the
// internal/analysis package directory where tests run).
func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() failed: %v", err)
	}
	// tests run from package dir: internal/analysis/
	// two levels up is module root
	return filepath.Join(wd, "../..")
}

func TestLoad_VTA_basic(t *testing.T) {
	root := moduleRoot(t)
	ctx := context.Background()

	result, err := analysis.Load(ctx, root, "./testdata/basic", "vta")
	if err != nil {
		t.Fatalf("Load(ctx, root, \"./testdata/basic\", \"vta\") returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Load(ctx, root, \"./testdata/basic\", \"vta\") = nil, want non-nil")
	}
	if result.SSAPkg == nil {
		t.Errorf("Load(...).SSAPkg = nil, want non-nil")
	}
	if result.Graph == nil {
		t.Errorf("Load(...).Graph = nil, want non-nil")
	}
	if result.Module != "github.com/shairoth12/trawl" {
		t.Errorf("Load(...).Module = %q, want %q", result.Module, "github.com/shairoth12/trawl")
	}
	if result.Graph != nil && len(result.Graph.Nodes) == 0 {
		t.Errorf("Load(...).Graph.Nodes has len 0, want > 0")
	}
}

func TestLoad_RTA_returnsNilGraph(t *testing.T) {
	root := moduleRoot(t)
	ctx := context.Background()

	result, err := analysis.Load(ctx, root, "./testdata/basic", "rta")
	if err != nil {
		t.Fatalf("Load(ctx, root, \"./testdata/basic\", \"rta\") returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Load(ctx, root, \"./testdata/basic\", \"rta\") = nil, want non-nil")
	}
	if result.Graph != nil {
		t.Errorf("Load(..., \"rta\").Graph = %v, want nil (RTA graph built by caller)", result.Graph)
	}
	if result.SSAPkg == nil {
		t.Errorf("Load(..., \"rta\").SSAPkg = nil, want non-nil")
	}
}

func TestLoad_nonexistentPath(t *testing.T) {
	root := moduleRoot(t)
	ctx := context.Background()

	_, err := analysis.Load(ctx, root, "./nonexistent", "vta")
	if err == nil {
		t.Errorf("Load(ctx, root, \"./nonexistent\", \"vta\") = nil error, want an error")
	}
}

func TestLoad_brokenPackage(t *testing.T) {
	tempDir := t.TempDir()

	gomod := "module example.com/broken\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}

	brokenGo := "package broken\n\nfunc oops() { _ = undeclaredVar }\n"
	if err := os.WriteFile(filepath.Join(tempDir, "broken.go"), []byte(brokenGo), 0o644); err != nil {
		t.Fatalf("WriteFile(broken.go): %v", err)
	}

	ctx := context.Background()
	_, err := analysis.Load(ctx, tempDir, ".", "vta")
	if err == nil {
		t.Fatalf("Load(ctx, tempDir, \".\", \"vta\") = nil error, want an error containing \"package load errors\"")
	}
	if got := err.Error(); !strings.Contains(got, "package load errors") {
		t.Errorf("Load(...) error = %q, want it to contain \"package load errors\"", got)
	}
}
