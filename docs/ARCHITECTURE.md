# Architecture

## Purpose

trawl is a Go static analysis CLI. Given a Go package and an entry-point function name, it builds a call graph using SSA intermediate representation and walks it via DFS to detect every external service call reachable from that function. Output is JSON.

## System Overview

```
trawl.yaml ──► loadConfig() ──► Config
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
              module, log  walker.Walk(fn) ──► []ExternalCall (short names filled)
                                                    │
                                                    ▼
                                           relativize paths
                                           dedup (--dedup)
                                                    │
                                                    ▼
                                             JSON ──► stdout
```

All stages run sequentially in `cmd/trawl/main.go`.
Data types are defined in the root `trawl` package (`trawl.go`); it holds types only (see [ADR 0013](adr/0013-root-package-is-schema-only.md)).
The `internal/` packages import `trawl` for shared types. `analysis` and `walker` also import `detector` for the mock and standard-library checks; nothing imports `walker` or `analysis`.

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
    │  go/packages loads every package (signatures + source).
    │
    │  Pick which packages get function bodies:
    │    - the --pkg / --scope packages
    │    - other packages of your module
    │    - dependency packages with a type implementing an interface
    │      your code calls (≤3 rounds, ≤200 packages)
    │
    │  Build bodies one package at a time
    │    (an SSA panic becomes an error, not a crash)
    │
    │  Build the call graph:
    │    CHA seed → VTA/CHA graph
    │    (RTA: nil graph here, built in Stage 5)
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
    │
    │    ┌─ Which package owns the callee? (calleePkg)
    │    │    First non-nil wins:
    │    │      1. fn.Package()            (ordinary function or method)
    │    │      2. fn.Origin().Package()   (generic instantiation, e.g. Map[string])
    │    │      3. fn.Object().Pkg()       (synthetic wrapper)
    │    │      4. receiver's named type   (last resort)
    │    │    all nil → skip edge
    │    │
    │    ├─ Very common interface? (error, io.Reader, ...) → skip
    │    │
    │    ├─ Mock method? (struct with a mock.Mock or *gomock.Controller field)
    │    │    real implementation reachable
    │    │    (same module, a dependency with bodies, or already inside one) → skip
    │    │    otherwise → guess the service from the mock package's imports
    │    │
    │    ├─ Detector match? (import path matches an indicator)
    │    │    → emit ExternalCall (direct, high confidence)
    │    │
    │    ├─ Inside your module? → recurse DFS
    │    │      findings are reported at the line where they happen
    │    │      (also when a dependency calls back into your code:
    │    │       the report points at your line, not at the dependency)
    │    │
    │    ├─ Dependency package with bodies? → enter it and keep walking
    │    │      anything found inside is reported at the call in your code
    │    │      that entered it (resolved_via: cross_module_trace)
    │    │
    │    └─ External package without bodies, called through an interface?
    │         → guess the service from the package's imports, then stop
    │
    │  After the edges:
    │    every interface call in this function that resolved
    │    to nothing (or only to mocks) →
    │      interface_dispatch record, classified by the package
    │      that declares the interface
    │
    │  Finally: several hits for the same line collapse into one record,
    │  then ShortFunction / ShortCallChain are filled in
    ▼
Stage 7: Post-process + JSON output
    │  ├─ Strip absolute file paths → relative
    │  ├─ Deduplicate (--dedup)
    │  └─ json.Encoder → stdout
    ▼
  EXIT
```

## Package Map

```
github.com/shairoth12/trawl/
│
├── trawl.go              Root package. Type definitions only, no functions.
│   │                     Result, ExternalCall, AnalysisStats, Indicator, Config
│   │                     ServiceType, ResolvedVia, Confidence + their constants
│   │
├── cmd/trawl/
│   ├── main.go           CLI entry point. Flag parsing, pipeline orchestration.
│   │                     buildLogger(), deduplicateCalls(), versionInfo(), toolchainWarning()
│   └── config.go         loadConfig(), validateConfig(): reads YAML, validates non-empty fields
│
├── internal/
│   ├── analysis/
│   │   ├── analysis.go   Load(ctx, Options): go/packages → SSA → call graph
│   │   │                 Options{Dir, Pattern, Algo, Scope, DependencyPolicy, MaxDependencyPkgs, IsIndicator}
│   │   │                 Algo type: "vta" | "rta" | "cha"; DependencyPolicy: "auto" | "none"
│   │   │                 createProgram(): ssautil.Packages clone with a wider "with bodies" set
│   │   │                 buildProgram(): per-package Build in bounded goroutines, panic → error
│   │   │                 ErrPackageLoad sentinel
│   │   │                 VTA pipeline: CHA seed → vta.CallGraph(allFns, chaGraph)
│   │   │                 CRITICAL: never call graph.DeleteSyntheticNodes()
│   │   │
│   │   ├── deps.go       selectDependencyPkgs(): picks which extra packages get bodies —
│   │   │                 other packages of your module, and dependency packages with a
│   │   │                 type implementing an interface your code calls (≤3 rounds);
│   │   │                 never stdlib or indicator packages
│   │   │
│   │   └── resolve.go    Resolve(): entry string → *ssa.Function
│   │                     3 formats: FuncName, Type.Method, BareMethod
│   │                     Mocks (mock.Mock / *gomock.Controller field) skipped in bare resolution
│   │
│   ├── detector/
│   │   ├── detector.go   Detector interface: Detect(importPath) → (ServiceType, bool)
│   │   │                 Prefix matching with boundary check (/ separator)
│   │   │                 SkipInternal: excludes /internal/ subpackages
│   │   │                 WrapperFor: expanded to separate indicators at New() time
│   │   │                 Priority: user indicators first, then builtins
│   │   │                 IsStandardLibrary(): dotless-first-element check
│   │   │
│   │   ├── builtin.go    13 built-in indicators (HTTP, gRPC, Redis, Postgres, etc.)
│   │   │                 All have SkipInternal: true
│   │   │
│   │   └── mockcheck.go  IsMock(), IsMockMethod(): generated-mock check (mock.Mock / *gomock.Controller field)
│   │
│   └── walker/
│       ├── walker.go     Walker: DFS traversal of callgraph.Graph
│       │                 4 emission sites (see Emission Sites below)
│       │                 Filters: ubiquitous interfaces, mock types
│       │                 Cross-module inference: 2-level transitive import scan
│       │                 appendCopy() prevents slice aliasing in DFS chains
│       │
│       ├── shorten.go    shortenName(): strips module paths and generics for short_* fields
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
│   ├── mock/, gomock/    Stand-ins for testify mock.Mock and gomock.Controller
│   ├── crossmodule/      Two modules: svc (analyzed) + lib (dependency)
│   ├── generic/          Generic type instantiation
│   └── scope/            VTA/CHA scope resolution (leaf + wiring)
│                         (YAML config fixtures live in cmd/trawl/testdata/config/)
│
├── integration_test.go   15 end-to-end tests (full pipeline)
├── trawl_test.go         Unit tests for root package types
├── go.mod                Module: github.com/shairoth12/trawl, Go 1.26
├── .golangci.yml         Linter config (v2 format)
├── .goreleaser.yaml      Cross-platform release builds
└── Makefile              build, test, lint, clean, release-dry-run
```

## Key Data Types

Which enum values may be added in a minor release: see [OUTPUT-FORMAT.md, Compatibility](OUTPUT-FORMAT.md#compatibility).

```
ServiceType  string                    // "HTTP", "REDIS", "GRPC", …
ResolvedVia  string                    // how a call was found: "direct", "cross_module_trace", …
Confidence   string                    // "high" | "medium" | "low"

Result {
    EntryPoint    string               // SSA-qualified: "pkg.FuncName"
    Package       string               // import path of analyzed package
    ExternalCalls []ExternalCall        // never nil in trawl's output
    Deduplicated  bool                 // true iff --dedup used
}

ExternalCall {
    ServiceType    ServiceType         // matched service label
    ImportPath     string              // Go import path of called package
    Function       string              // full SSA function name
    File           string              // relative source path
    Line           int                 // 0 for synthetic edges
    CallChain      []string            // entry → … → call site
    ResolvedVia    ResolvedVia         // open enum: "direct" | "mock_inference" | "cross_module_inference" | "cross_module_trace" | "interface_dispatch" | future values
    Confidence     Confidence          // closed enum: "high" | "medium" | "low"
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
    DependencyPolicy  DependencyPolicy       // "auto" (default) | "none"
    MaxDependencyPkgs int                    // 0 → 200
    IsIndicator       func(string) bool      // indicator packages never get bodies
}
```

## Where Records Come From

A few terms used below:

- **body** — the code of a function, as opposed to its signature. trawl can only follow calls inside functions whose bodies were built.
- **initial packages** — the packages named by `--pkg` and `--scope`. Their bodies are always built.
- **dependency package** — any other package trawl decided to build bodies for (same-module packages and dependency-module packages that implement an interface your code calls).
- **interface call** — a call like `h.store.Get(...)` where `store` is an interface, so the concrete method is only known if the call graph can resolve it.
- **entering a dependency** — the walk followed a call from your code into a dependency package's body. Anything found there is reported at that call in your code, not at the line deep inside the dependency.
- **indicator package** — a package the detector already classifies as a service (built-in list or `trawl.yaml`).

Every record is created by `record()`. In your own code it points at the call
that was made; inside a dependency it points at the call in your code that
entered the dependency. The distinct sources:

```
Site │ When                                                        │ resolved_via            │ confidence
─────┼─────────────────────────────────────────────────────────────┼─────────────────────────┼───────────
 M   │ interface call resolved to a mock type in an external       │ mock_inference          │ medium
     │ package without bodies                                      │ (direct if pkg is an    │ (high)
     │                                                             │  indicator)             │
 D   │ callee's package is an indicator (your code)                │ direct                  │ high
 T   │ a backend call found inside a dependency package            │ cross_module_trace      │ same as the backend hit
 X   │ interface call into an external package without bodies      │ cross_module_inference  │ low
 X'  │ entered a dependency, found nothing, fell back to its       │ cross_module_inference  │ low
     │ imports                                                     │                         │
 I   │ interface call that resolved to nothing (or only mocks)     │ interface_dispatch      │ high if the interface's
     │                                                             │                         │ package is an indicator,
     │                                                             │                         │ low if guessed from imports
```

Site I ignores `error`, standard-library interfaces and very common ones
(`io.Reader`, `fmt.Stringer`, ...). Interfaces declared in your own module are
counted in `unresolved_invokes` but never reported.

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

Decisions that are hard to reverse, surprising without context, and the result of a real trade-off live as Architecture Decision Records under [`docs/adr/`](adr/), one file per decision, numbered in the order they were made. New decisions get the next number; a reversed decision gets a new record marked `superseded by`, the old one is never edited.

| ADR | Decision |
|-----|----------|
| [0001](adr/0001-never-delete-synthetic-nodes.md) | Never call `graph.DeleteSyntheticNodes()` |
| [0002](adr/0002-cha-seed-then-vta.md) | VTA pipeline: CHA seed, then VTA refinement |
| [0003](adr/0003-detector-before-module-boundary.md) | Detector runs before the module-boundary check |
| [0004](adr/0004-appendcopy-for-call-chains.md) | `appendCopy` for DFS call chains |
| [0005](adr/0005-empty-module-means-no-boundary.md) | An empty module path disables the boundary |
| [0006](adr/0006-rta-graph-built-after-resolution.md) | RTA graph is built after entry-point resolution |
| [0007](adr/0007-method-resolution-via-types-named.md) | Method resolution through `types.Named.Methods()` |
| [0008](adr/0008-generic-package-via-origin.md) | Generic instantiations recover their package via `Origin()` |
| [0009](adr/0009-selective-dependency-bodies.md) | Only some dependency packages get function bodies |
| [0010](adr/0010-attribute-to-boundary-call-site.md) | Findings inside a dependency are reported at the call in your code that entered it |
| [0011](adr/0011-always-on-position-merge.md) | Position merge is always on |
| [0012](adr/0012-per-package-build-not-program-build.md) | `Program.Build` is not used |
| [0013](adr/0013-root-package-is-schema-only.md) | Root package holds types only |

## Test Strategy

```
Level          │ Files                          │ Count │ What it validates
───────────────┼────────────────────────────────┼───────┼──────────────────────────────────
Unit           │ trawl_test.go                  │ 4     │ Type serialization
Unit           │ internal/detector/*_test.go    │ 9     │ Prefix matching, SkipInternal, WrapperFor, builtins, stdlib check
Unit           │ internal/analysis/*_test.go    │ 22    │ Package loading, dependency selection, SSA build, entry resolution
Unit           │ internal/walker/*_test.go      │ 31    │ DFS traversal, filters, inference, generics, walking into dependencies, merge, name shortening
Unit           │ cmd/trawl/*_test.go            │ 15    │ Version info, dedup, help output, --deps, stats, config loading
Integration    │ integration_test.go            │ 15    │ Full pipeline: load → resolve → walk → JSON (incl. two-module fixture)
```

Tests use `t.Parallel()` at both top and subtest levels except those that call `analysis.Load` more than once per function. The two-module fixture under `testdata/crossmodule/` (`svc` analyzed module + `lib` dependency module, wired by `replace`) drives the dependency-body tests. Table-driven tests are the norm. The integration tests use a `pipeline()` helper that runs the entire analysis chain against `testdata/` fixtures.
