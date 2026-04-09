# Architecture

## Purpose

trawl is a Go static analysis CLI. Given a Go package and an entry-point function name, it builds a call graph using SSA intermediate representation and walks it via DFS to detect every external service call reachable from that function. Output is JSON.

## System Overview

```
trawl.yaml ──► LoadConfig() ──► Config
                                  │
                                  ▼
./pkg + scope ──► analysis.Load() ──► LoadResult { Prog, Graph, SSAPkg, Module }
                                          │
                                          ▼
         entry ──► analysis.Resolve() ──► *ssa.Function
                                              │
                              ┌───────────────┤
                              │ (RTA only)    │
                              ▼               │
                      rta.Analyze()           │
                          │ graph             │
                          └───────┬───────────┘
                                  │
      Config.Indicators ──► detector.New() ──► Detector
                                                  │
                                                  ▼
      graph + Detector ──► walker.New() ──► Walker
              module, log  walker.Walk(fn) ──► []ExternalCall
                                                    │
                                                    ▼
                                           relativize paths
                                           dedup (--dedup)
                                           ShortenName
                                                    │
                                                    ▼
                                             JSON ──► stdout
```

All stages run sequentially in `cmd/trawl/main.go`.
Data types are defined in the root `trawl` package (`trawl.go`, `config.go`).
The three `internal/` packages import `trawl` for shared types but do not import each other.

## Pipeline

The analysis runs as a fixed 7-stage pipeline. Every invocation executes these stages in order:

```
Stage 1: Parse CLI flags + build logger
    │  --log-level, --log-file, --log-format → *slog.Logger → stderr or file
    ▼
Stage 2: Load config (YAML → Config struct)
    │
    ▼
Stage 3: Load packages + build SSA + construct call graph
    │  go/packages → ssa.Program → CHA seed → VTA/CHA graph
    │  (or nil graph for RTA — deferred to after resolution)
    ▼
Stage 4: Resolve entry point
    │  entry string → *ssa.Function
    │  Format: "FuncName" | "Type.Method" | "BareMethod"
    ▼
Stage 5: Build RTA graph (only when --algo rta)
    │  rta.Analyze([]*ssa.Function{fn}, true) → graph
    ▼
Stage 6: DFS Walk
    │  walker.New(graph, detector, module, fset, log)
    │  walker.Walk(entry) → []ExternalCall
    │
    │  For each edge in the call graph:
    │    ┌─ Is package nil? (generic instantiation)
    │    │    → recover receiver pkg path
    │    │    → same-module? recurse : classify external generic
    │    │
    │    ├─ Ubiquitous interface dispatch? (error, io.Reader, etc.)
    │    │    → skip
    │    │
    │    ├─ Mock type method? (type name starts with "Mock")
    │    │    → skip (same-module) or infer from imports (external)
    │    │
    │    ├─ Detector match? (import path matches indicator)
    │    │    → emit ExternalCall (direct, high confidence)
    │    │
    │    ├─ Outside module boundary?
    │    │    → attempt cross-module inference via transitive imports
    │    │    → stop recursion
    │    │
    │    └─ Inside module boundary
    │         → recurse DFS
    ▼
Stage 7: Post-process + JSON output
    │  ├─ Strip absolute file paths → relative
    │  ├─ Deduplicate (--dedup)
    │  ├─ Populate ShortFunction / ShortCallChain
    │  └─ json.Encoder → stdout
    ▼
  EXIT
```

## Package Map

```
github.com/shairoth12/trawl/
│
├── trawl.go              Root package. Type definitions only.
│   │                     Result, ExternalCall, ServiceType, Indicator, Config
│   │                     ShortenName() — strips module paths and generics
│   │
├── config.go             LoadConfig(), Config.Validate()
│   │                     Reads YAML, validates non-empty fields
│   │
├── cmd/trawl/
│   └── main.go           CLI entry point. Flag parsing, pipeline orchestration.
│                         buildLogger(), deduplicateCalls(), versionInfo(), toolchainWarning()
│
├── internal/
│   ├── analysis/
│   │   ├── analysis.go   Load(): go/packages → SSA → call graph
│   │   │                 Algo type: "vta" | "rta" | "cha"
│   │   │                 ErrPackageLoad sentinel
│   │   │                 VTA pipeline: CHA seed → vta.CallGraph(allFns, chaGraph)
│   │   │                 CRITICAL: never call graph.DeleteSyntheticNodes()
│   │   │
│   │   └── resolve.go    Resolve(): entry string → *ssa.Function
│   │                     3 formats: FuncName, Type.Method, BareMethod
│   │                     Mock types (name starts "Mock") skipped in bare resolution
│   │
│   ├── detector/
│   │   ├── detector.go   Detector interface: Detect(importPath) → (ServiceType, bool)
│   │   │                 Prefix matching with boundary check (/ separator)
│   │   │                 SkipInternal: excludes /internal/ subpackages
│   │   │                 WrapperFor: expanded to separate indicators at New() time
│   │   │                 Priority: user indicators first, then builtins
│   │   │
│   │   └── builtin.go    13 built-in indicators (HTTP, gRPC, Redis, Postgres, etc.)
│   │                     All have SkipInternal: true
│   │
│   └── walker/
│       ├── walker.go     Walker: DFS traversal of callgraph.Graph
│       │                 4 emission sites (see Emission Sites below)
│       │                 Filters: ubiquitous interfaces, mock types
│       │                 Cross-module inference: 2-level transitive import scan
│       │                 appendCopy() prevents slice aliasing in DFS chains
│       │
│       └── export_test.go  Test bridge — exports unexported helpers
│
├── testdata/             Fixture packages (one per scenario)
│   ├── basic/            Direct HTTP call
│   ├── chain/            3-layer handler→service→repo
│   ├── cycle/            Mutual recursion
│   ├── goroutine/        go func(){} closure
│   ├── iface/            VTA interface dispatch
│   ├── multi/            HTTP + database/sql
│   ├── resolve/          Entry resolution edge cases
│   ├── erriface/         Ubiquitous dispatch filtering
│   ├── mockfilter/       Mock type filtering
│   ├── generic/          Generic type instantiation
│   ├── scope/            VTA/CHA scope resolution (leaf + wiring)
│   └── config/           YAML config fixtures
│
├── integration_test.go   13 end-to-end tests (full pipeline)
├── trawl_test.go         Unit tests for root package types
├── go.mod                Module: github.com/shairoth12/trawl, Go 1.25
├── .golangci.yml         Linter config (v2 format)
├── .goreleaser.yaml      Cross-platform release builds
└── Makefile              build, test, lint, clean, release-dry-run
```

## Key Data Types

```
ServiceType  string                    // "HTTP", "REDIS", "GRPC", …

Result {
    EntryPoint    string               // SSA-qualified: "pkg.FuncName"
    Package       string               // import path of analyzed package
    ExternalCalls []ExternalCall        // never nil
    Deduplicated  bool                 // true iff --dedup used
}

ExternalCall {
    ServiceType    ServiceType         // matched service label
    ImportPath     string              // Go import path of called package
    Function       string              // full SSA function name
    File           string              // relative source path
    Line           int                 // 0 for synthetic edges
    CallChain      []string            // entry → … → call site
    ResolvedVia    string              // "direct" | "mock_inference" | "cross_module_inference"
    Confidence     string              // "high" | "medium" | "low"
    ShortFunction  string              // Function with paths/generics stripped
    ShortCallChain []string            // CallChain with same stripping
}

Indicator {
    Package      string                // import path prefix to match
    ServiceType  ServiceType           // label to assign
    SkipInternal bool                  // exclude /internal/ subpkgs
    WrapperFor   []string              // extra prefixes → same ServiceType
}

Config {
    Indicators []Indicator
}

LoadResult {
    Prog   *ssa.Program
    Graph  *callgraph.Graph            // nil for RTA until rta.Analyze
    SSAPkg *ssa.Package
    Module string                      // from go.mod
}

Algo  string                           // "vta" | "rta" | "cha"
```

## Walker Emission Sites

The DFS walker has 4 distinct code paths that emit `ExternalCall` records:

```
Site │ Condition                                │ ResolvedVia              │ Confidence
─────┼──────────────────────────────────────────┼──────────────────────────┼───────────
 G   │ pkg==nil, external generic, invoke edge  │ varies (default→upgrade)│ varies
 2   │ pkg!=nil, isMockMethod, external module  │ mock_inference or direct│ medium/high
 3   │ pkg!=nil, det.Detect() matches           │ direct                  │ high
 4   │ pkg!=nil, outside module, invoke edge    │ cross_module_inference  │ low
```

Site G and Site 2 use a default-then-upgrade pattern:
1. Set default (cross_module_inference/low or mock_inference/medium)
2. If `isMockReceiver(fn)` → mock_inference/medium
3. If `det.Detect(path)` matches → direct/high (overrides previous)

## Dependency Graph

```
cmd/trawl/main.go
  ├── trawl           (root types + config)
  ├── internal/analysis (Load, Resolve)
  ├── internal/detector (New, Detect)
  ├── internal/walker   (New, Walk)
  └── x/tools/go/callgraph/rta (RTA-specific)

internal/analysis
  ├── x/tools/go/packages
  ├── x/tools/go/ssa
  ├── x/tools/go/ssa/ssautil
  ├── x/tools/go/callgraph/cha
  └── x/tools/go/callgraph/vta

internal/detector
  └── trawl (Indicator, ServiceType)

internal/walker
  ├── trawl (ExternalCall, ServiceType, constants)
  ├── internal/detector (Detector interface)
  ├── x/tools/go/callgraph
  └── x/tools/go/ssa
```

## Critical Design Decisions

### 1. Never call `graph.DeleteSyntheticNodes()`

The VTA pipeline produces a graph with synthetic nodes. Calling `DeleteSyntheticNodes()` strips direct call edges — it reduces a ~6000-node graph to ~180 nodes with no edges from entry points to stdlib. The walker handles synthetic nodes implicitly via the module-boundary and detector checks.

### 2. VTA pipeline: CHA seed then VTA refinement

```
CHA seed graph = cha.CallGraph(prog)
VTA graph      = vta.CallGraph(allFunctions, CHA seed)
```

CHA alone over-approximates. VTA refines by tracking value flow. The CHA seed provides initial edges that VTA then prunes.

### 3. Detector runs BEFORE module-boundary check

If detector match were after the boundary check, calls to third-party libraries would be silently skipped (they're outside the module). Running detector first ensures all matching external calls are captured.

### 4. `appendCopy` prevents DFS chain aliasing

Go's `append` reuses the underlying array when capacity allows. In DFS with branching, siblings would corrupt each other's chains. `appendCopy` always allocates a new slice.

### 5. Module boundary with empty module

When `Module` from `go.mod` is empty (GOPATH), `strings.HasPrefix(pkgPath, "")` is always true — the walker recurses without bound. This is the correct GOPATH fallback.

### 6. RTA graph built after resolution

RTA requires entry-point roots. `LoadResult.Graph` is nil until `main.go` calls `rta.Analyze([]*ssa.Function{fn}, true)`. This is by design, not a bug.

### 7. SSA method resolution via `types.Named.Methods()`

Methods are NOT in `ssaPkg.Members` keyed by `(*TypeName).Method`. The correct path:
```
ssaPkg.Members[typeName] → (*ssa.Type) → .Type() → (*types.Named) → .Methods() iterator → prog.FuncValue(method)
```

### 8. Generic instantiations have `fn.Package() == nil`

Go SSA sets `Package()` to nil for all generic type instantiations. The walker recovers the package path from the receiver's `*types.Named` type via `receiverPkgPath(fn)`.

## Test Strategy

```
Level          │ Files                          │ Count │ What it validates
───────────────┼────────────────────────────────┼───────┼──────────────────────────────────
Unit           │ trawl_test.go                  │ 19    │ Type serialization, ShortenName, Config validation
Unit           │ internal/detector/*_test.go    │ 8     │ Prefix matching, SkipInternal, WrapperFor, builtins
Unit           │ internal/analysis/*_test.go    │ 12    │ Package loading, SSA build, entry resolution
Unit           │ internal/walker/walker_test.go │ 12    │ DFS traversal, filters, inference, generics
Unit           │ cmd/trawl/main_test.go         │ 7     │ Version info, dedup, help output
Integration    │ integration_test.go            │ 13    │ Full pipeline: load → resolve → walk → JSON
```

All tests use `t.Parallel()` at both top and subtest levels. Table-driven tests are the norm. The integration tests use a `pipeline()` helper that runs the entire analysis chain against `testdata/` fixtures.
