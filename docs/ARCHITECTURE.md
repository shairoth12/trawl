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
    │  go/packages → select dependency packages (same-module + implementors
    │  of invoked interfaces, ≤3 rounds, ≤200) → ssa.Program with bodies for
    │  initial + selected packages (built per package; panics become errors)
    │  → CHA seed → VTA/CHA graph (or nil graph for RTA — deferred)
    ▼
Stage 4: Resolve entry point
    │  entry string → *ssa.Function
    │  Format: "FuncName" | "Type.Method" | "BareMethod"
    ▼
Stage 5: Build RTA graph (only when --algo rta)
    │  rta.Analyze([]*ssa.Function{fn}, true) → graph
    ▼
Stage 6: DFS Walk
    │  walker.New(graph, detector, walker.Options{Module, DependencyPkgs, Fset, Log})
    │  walker.Walk(entry) → []ExternalCall
    │
    │  For each edge in the call graph:
    │    ┌─ calleePkg(fn): Package(), else Origin() (generic instantiation),
    │    │    else Object(), else receiver type; none → skip
    │    │
    │    ├─ Ubiquitous interface dispatch? (error, io.Reader, etc.)
    │    │    → skip
    │    │
    │    ├─ Mock type method? (type name starts with "Mock")
    │    │    → skip when the real implementation is available
    │    │      (same-module, built dependency, or inside dependency code);
    │    │      otherwise infer from imports (external, invoke edge)
    │    │
    │    ├─ Detector match? (import path matches indicator)
    │    │    → emit ExternalCall (direct, high confidence)
    │    │
    │    ├─ Inside module boundary → recurse DFS (attribution resets to
    │    │    module-side call sites, also for callbacks from dependencies)
    │    │
    │    ├─ Built dependency package → open a crossing (or continue the
    │    │    active one) and recurse; findings attribute to the boundary
    │    │    call site as cross_module_trace
    │    │
    │    └─ Body-less external, invoke edge
    │         → cross-module inference via transitive imports, stop
    │
    │  After the edges: every interface call site in the node with no
    │  non-mock callee → classifyUnresolved → interface_dispatch record
    │  Finally mergeByPosition collapses hits for one call site
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
│   │   ├── analysis.go   Load(ctx, Options): go/packages → SSA → call graph
│   │   │                 Options{Dir, Pattern, Algo, Scope, Deps, MaxDependencyPkgs, IsIndicator}
│   │   │                 Algo type: "vta" | "rta" | "cha"; DepPolicy: "auto" | "none"
│   │   │                 createProgram(): ssautil.Packages clone with a wider "with bodies" set
│   │   │                 buildProgram(): per-package Build in bounded goroutines, panic → error
│   │   │                 ErrPackageLoad sentinel
│   │   │                 VTA pipeline: CHA seed → vta.CallGraph(allFns, chaGraph)
│   │   │                 CRITICAL: never call graph.DeleteSyntheticNodes()
│   │   │
│   │   ├── deps.go       selectDependencyPkgs(): same-module packages + implementors of
│   │   │                 invoked interfaces, ≤3 rounds; never stdlib or indicator packages
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
    ResolvedVia    string              // "direct" | "mock_inference" | "cross_module_inference" | "cross_module_trace" | "interface_dispatch"
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
    Prog                  *ssa.Program
    Graph                 *callgraph.Graph   // nil for RTA until rta.Analyze
    SSAPkg                *ssa.Package
    Module                string             // from go.mod
    PackagesLoaded        int
    DependencyPkgs        []string           // non-initial packages with bodies
    DependencyPkgsSkipped int                // dropped by MaxDependencyPkgs
    PackagesAnalyzed      int                // initial + len(DependencyPkgs)
}

Options {
    Dir, Pattern      string
    Algo              Algo                   // "vta" | "rta" | "cha"
    Scope             []string
    Deps              DepPolicy              // "auto" (default) | "none"
    MaxDependencyPkgs int                    // 0 → 200
    IsIndicator       func(string) bool      // indicator packages never get bodies
}
```

## Walker Emission Sites

Every emission goes through `record()`, which attributes a hit either to the
edge's own call site (module code) or to the active crossing's boundary site
(dependency code). The distinct sources:

```
Site │ Condition                                          │ ResolvedVia             │ Confidence
─────┼────────────────────────────────────────────────────┼─────────────────────────┼───────────
 M   │ isMockMethod, external, body-less, invoke edge     │ mock_inference or direct│ medium/high
 D   │ det.Detect() matches, in module code               │ direct                  │ high
 T   │ any evidence found while a crossing is active      │ cross_module_trace      │ that of the evidence
 X   │ body-less external callee, invoke edge             │ cross_module_inference  │ low
 X'  │ built dependency walked, nothing found, invoke edge│ cross_module_inference  │ low
 I   │ interface call with no non-mock callee             │ interface_dispatch      │ high (indicator pkg) / low (inferred)
```

Site M upgrades mock_inference/medium to direct/high when `det.Detect(path)`
matches the mock's package. Site I ignores universe, stdlib and ubiquitous
interfaces, and only counts (never reports) same-module interfaces.

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

Go SSA sets `Package()` to nil for all generic instantiations. `calleePkg(fn)` recovers the package from `fn.Origin()` (the generic declaration, which has a package), then `fn.Object()`, then the receiver's `*types.Named`. Top-level generic helpers (`Map[T, U]`) therefore no longer drop their edges.

### 9. Dependency bodies are selected, not blanket-built

Building bodies for all transitive packages is unbounded (thousands of packages). `selectDependencyPkgs` picks packages that declare a concrete, non-mock implementor of an interface the analyzed code invokes — a types-level pass over `TypesInfo.Selections` — and repeats up to 3 rounds so facade → client-wrapper chains resolve. A second SSA build is impossible: `CreatePackage` must be called once per `*types.Package`, so the selection happens before `ssa.NewProgram`.

### 10. Findings inside dependency code attribute to the boundary call site

The "what do I mock" use case needs the module-side line. A `crossing` carries the boundary call's position, callee and package; `record()` rewrites every hit made under it to that site with `resolved_via: cross_module_trace`, while `call_chain` keeps the path into the dependency.

### 11. Position merge is always on

CHA yields several hits for one source call (concrete body + mock edge; QueryRowContext + Scan). `mergeByPosition` collapses hits with the same service type and call-site position — higher confidence wins, ties keep the interface label — before `--dedup` runs.

### 12. `Program.Build` is not used

`Program.Build` runs each `Package.Build` in its own goroutine; a panic there cannot be recovered by the caller. `buildProgram` drives `Package.Build` itself in bounded goroutines and converts a panic into an error that names `--deps none`.

## Test Strategy

```
Level          │ Files                          │ Count │ What it validates
───────────────┼────────────────────────────────┼───────┼──────────────────────────────────
Unit           │ trawl_test.go                  │ 19    │ Type serialization, ShortenName, Config validation
Unit           │ internal/detector/*_test.go    │ 8     │ Prefix matching, SkipInternal, WrapperFor, builtins
Unit           │ internal/analysis/*_test.go    │ 22    │ Package loading, dependency selection, SSA build, entry resolution
Unit           │ internal/walker/*_test.go      │ 30    │ DFS traversal, filters, inference, generics, crossings, merge
Unit           │ cmd/trawl/main_test.go         │ 12    │ Version info, dedup, help output, --deps, stats
Integration    │ integration_test.go            │ 15    │ Full pipeline: load → resolve → walk → JSON (incl. two-module fixture)
```

Tests use `t.Parallel()` at both top and subtest levels except those that call `analysis.Load` more than once per function. The two-module fixture under `testdata/crossmodule/` (`svc` analyzed module + `lib` dependency module, wired by `replace`) drives the dependency-body tests. Table-driven tests are the norm. The integration tests use a `pipeline()` helper that runs the entire analysis chain against `testdata/` fixtures.
