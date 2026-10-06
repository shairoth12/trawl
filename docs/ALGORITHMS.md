# Call Graph Algorithms

trawl supports three call graph construction algorithms via the `--algo` flag. This document explains when to use each one.

## Quick Decision Guide

```
What kind of DI does your codebase use?

├─ No DI / all concrete types visible in analyzed package
│   └─ use: --algo vta (default)
│
├─ Constructor injection (NewServer(NewStore()), or wire's generated code)
│   └─ use: --algo vta --scope ./cmd/server
│      (scope loads the wiring package so VTA can trace value flow)
│
├─ Reflection-based DI (dig, fx)
│   └─ use: --algo vta (default)
│      (calls with no value flow fall back to CHA callees)
│      if interface_dispatch records remain: --algo cha
│
└─ Unsure / want broadest coverage
    └─ use: --algo cha --scope ./...
       (over-approximates, but catches everything)
```

## Algorithm Comparison

```
Property          │ VTA                  │ RTA                  │ CHA
──────────────────┼──────────────────────┼──────────────────────┼──────────────────────
Full name         │ Variable Type        │ Rapid Type           │ Class Hierarchy
                  │ Analysis             │ Analysis             │ Analysis
Precision         │ HIGH                 │ MEDIUM               │ LOW
                  │ Tracks value flow    │ Tracks reachable     │ Structural type
                  │ through variables    │ types from entry     │ matching only
Speed             │ Slower               │ Faster               │ Fastest
Graph built when  │ During Load()        │ After Resolve()      │ During Load()
Requires entry    │ NO (whole-program)   │ YES (entry roots)    │ NO (whole-program)
Interface resolve │ By observed value    │ By instantiated      │ By any structural
                  │ flow assignments     │ concrete types       │ implementor
Reflection DI     │ CHA fallback for     │ CANNOT trace         │ RESOLVES (by type
                  │ calls with no flow   │ reflect.Call         │ structure)
False positive    │ LOW                  │ LOW                  │ HIGHER (mitigated
risk              │                      │                      │ by filters)
```

## VTA (Variable Type Analysis) — Default

**How it works**: Builds a whole-program CHA graph as a seed, then refines it by tracking how concrete types flow through variables, function parameters, and return values. Only reports interface dispatch edges where the concrete type was actually assigned to the interface variable in observable code.

**Pipeline**:
```
cha.CallGraph(prog)                    ← seed: all structural matches
    │
    ▼
vta.CallGraph(allFunctions, chaGraph)  ← refinement: prune by value flow
    │
    ▼
fillEmptyInvokes(vtaGraph, chaGraph)   ← interface calls left with no callee get the CHA ones
```

**CHA fallback**: VTA gives an interface call no callees (or only mock callees) when no real value visibly reaches it, which is what reflection-based DI (dig, fx) looks like. For those calls only, trawl copies the CHA callees into the VTA graph, so the walk still enters the implementation (and the dependency bodies built for it). Calls on interfaces declared in the standard library (and `error`) are not filled: CHA would match every implementation in the program. Calls where VTA found at least one callee are left as VTA found them.

**When to use**:
- Default choice for most codebases
- When concrete types are wired via constructors in visible code
- When precision matters more than coverage

**Limitation**: Cannot trace through `reflect.Call` or `interface{}`/`any` type assertions at runtime. Interfaces filled by a DI container get the CHA fallback above, with CHA's precision and its precondition (the concrete type must be converted to an interface somewhere in built code).

**With `--scope`**: Loading extra packages gives VTA more value-flow edges to observe. Requires explicit value flow in the loaded code (e.g., a `Wire()` function that calls `HandleLeaf(ctx, &SQLStore{})`).

## RTA (Rapid Type Analysis)

**How it works**: Starts from the entry point function(s) and expands the set of reachable types as it discovers `new`/`make` allocations and interface satisfactions. Only considers types that are actually instantiated along reachable code paths.

**Pipeline**:
```
Load() → Graph is nil
Resolve() → *ssa.Function
rta.Analyze([]*ssa.Function{fn}, true) → graph
```

**When to use**:
- When you want faster analysis than VTA
- When you only care about types reachable from a specific entry point
- When the entry point transitively reaches all relevant concrete types

**Limitation**: Requires the entry point to be specified before graph construction. If an interface implementation is only instantiated in unreachable code, RTA won't see it.

## CHA (Class Hierarchy Analysis)

**How it works**: Resolves every interface dispatch to ALL concrete types in the program that structurally satisfy the interface. No value-flow or reachability analysis — purely type-matching.

**Pipeline**:
```
cha.CallGraph(prog) → graph  (used directly, no VTA refinement)
```

**When to use**:
- When you want the broadest possible coverage
- When `--algo vta` still reports `interface_dispatch` records: VTA keeps its own callees for a call where some value does flow, so an implementation bound only by reflection at that call is missed

**Precondition**: CHA only considers concrete types that are *runtime types* of the program — some **built** function body must convert the type to an interface (`MakeInterface`): a constructor returning the interface, or `var _ I = (*T)(nil)`. A dependency whose constructor returns the concrete type and is bound to the interface only through reflection degrades to an `interface_dispatch` record.

**Trade-off**: Over-approximates. CHA reports `Store.Get` being dispatched to `MockStore.Get` even if `MockStore` is never used at runtime. trawl mitigates this with filters:

### CHA False-Positive Filters

```
Filter                      │ What it catches                         │ How
────────────────────────────┼─────────────────────────────────────────┼────────────────────
Ubiquitous interface filter │ error.Error(), fmt.Stringer.String()    │ Skip dispatch on
                            │ io.Reader.Read(), context.Context, etc. │ known noisy interfaces
Mock type filter            │ (*MockStore).Get(), (*MockClient).Do()  │ Skip structs with a
                            │                                         │ mock.Mock or
                            │                                         │ *gomock.Controller field
Interface method labeling   │ Shows Store.Get not MockStore.Get       │ interfaceMethodLabel()
Cross-module inference      │ Wrapper pkgs (rediscache → go-redis)    │ 2-level import scan
```

## Dependency Bodies (`--deps`)

By default (`--deps auto`) trawl builds SSA function bodies not only for `--pkg` and `--scope` packages but also for:

- every other package of the analyzed module that the target imports, and
- dependency packages that declare a concrete, non-mock implementor of an interface the analyzed code invokes — selected in up to 3 rounds (so a facade's own interface dependencies resolve too), capped at 200 packages, never stdlib or indicator-matching packages.

The walker recurses into those bodies and reports what it finds at the module-side call site (`resolved_via: cross_module_trace`). Interfaces implemented in a dependency module therefore resolve **without** adding the dependency to `--scope`. `--deps none` restores body construction for the initial packages only.

**Example.** "Body" means the function's code, as opposed to its signature. Your handler calls `h.store.Get(ctx, key)` where `store.Store` is an interface from `example.com/lib/store`, and the only real implementation is `(*sqlStore).Get`, which calls `database/sql`:

```
--deps none   lib/store loaded as signatures only
              HandleGet ─► Store.Get ─► (*sqlStore).Get [no body: dead end]      → external_calls: []

--deps auto   round 1: svc calls store.Store → lib/store declares *sqlStore → build its bodies
              HandleGet ─► Store.Get ─► (*sqlStore).Get ─► sql.DB.QueryRowContext → POSTGRES,
                                                                                    cross_module_trace,
                                                                                    reported at HandleGet's line
```

If `(*sqlStore).Get` itself called a second interface (say `search.Searcher`) implemented in `lib/search`, round 2 would build `lib/search` the same way. That is what the rounds are for.

Interface calls that still have no concrete callee are reported as `interface_dispatch` records (high confidence when the interface is declared in an indicator package, low when inferred from that package's imports) and counted in `stats.unresolved_invokes`. RTA still cannot resolve reflection-based DI, but it now yields these hints instead of silence; VTA falls back to CHA for such calls (see [VTA](#vta-variable-type-analysis--default)).

**Tuning the selection limits**: 3 rounds and the 200-package cap are heuristics, not measured optima (see [ADR 0009](adr/0009-selective-dependency-bodies.md)). To check them against a real target, run with `--stats` twice — once as-is and once with `--deps none` — and compare `dependency_packages`, `packages_analyzed` and `load_duration_ms`. A `dependency_bodies_truncated` warning in the logs means the cap was hit; a large `unresolved_invokes` with a small `dependency_packages` means the rounds ran out before the chain resolved.

**Limitations**

- **Three rounds only.** An interface chain deeper than three hops inside dependencies is not traced; the last hop is reported as `interface_dispatch` instead.
- **Plain calls into external packages without bodies are skipped.** Only calls made through an interface get the imports-based guess. A direct `lib.DoThing()` into a package that has no bodies and is not an indicator produces nothing.
- **`nodes_visited` over-counts dependency functions.** A dependency function is counted again each time it is reached from a different call in your code.
- **Dotless GOPATH paths look like standard library.** A package is standard library when the loader reports no module for it and the first element of its import path has no dot (Go's own rule). In module mode every non-stdlib package has a module, so `module svc` is handled correctly. In GOPATH mode no package has a module, so a dotless GOPATH package is mistaken for standard library: it never gets a body and its interfaces are not reported.

## `--scope` Flag

`--scope` loads additional packages *as initial packages* to enrich the type universe. The primary package (`--pkg`) remains the analysis target. Since same-module packages imported by the target and implementor dependency packages are built automatically, `--scope` is needed for **wiring** packages the target does not import (e.g. `cmd/server` wiring a constructor-injected handler).

```
Without scope:    packages.Load("./internal/handler")
                  → only handler's direct imports visible

With scope:       packages.Load("./internal/handler", "./cmd/server")
                  → server's types also visible to the graph builder
```

**Interaction with algorithms**:

```
                    │ Without scope                │ With scope
────────────────────┼──────────────────────────────┼─────────────────────────────
VTA                 │ Only traces value flow in    │ Expanded type universe;
                    │ directly loaded packages     │ still requires visible value
                    │                              │ flow (constructor calls)
CHA                 │ Only matches types in        │ All loaded types eligible;
                    │ directly loaded packages     │ resolves DI without value flow
RTA                 │ Reachable from entry only    │ More types available if
                    │                              │ entry transitively reaches them
```

**Comma-separated patterns**:
```bash
--scope "./cmd/server,./internal/wiring"
```

**Wildcard**:
```bash
--scope "./..."   # loads entire module
```

## Decision Matrix

```
Scenario                                    │ Recommended flags
────────────────────────────────────────────┼──────────────────────────────────────────
Simple handler, no DI                       │ --algo vta
Handler with constructor DI (visible wiring)│ --algo vta --scope ./cmd/server
Handler with dig/fx DI                      │ --algo vta (--algo cha if interface_dispatch remains)
Maximum coverage, accept false positives    │ --algo cha --scope ./...
Fast analysis, good-enough precision        │ --algo rta
Analyzing a leaf package in isolation       │ --algo vta (no external calls expected)
Leaf package + want to see injected deps    │ --algo vta --scope ./path/to/wiring
```
