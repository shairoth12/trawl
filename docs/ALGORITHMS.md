# Call Graph Algorithms

trawl supports three call graph construction algorithms via the `--algo` flag. This document explains when to use each one.

## Quick Decision Guide

```
What kind of DI does your codebase use?

├─ No DI / all concrete types visible in analyzed package
│   └─ use: --algo vta (default)
│
├─ Manual constructor injection (NewServer(NewStore()))
│   └─ use: --algo vta --scope ./cmd/server
│      (scope loads the wiring package so VTA can trace value flow)
│
├─ Reflection-based DI (dig, fx, wire)
│   └─ use: --algo cha --scope ./...
│      (CHA resolves by structural type matching, no value flow needed)
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
Reflection DI     │ CANNOT trace         │ CANNOT trace         │ RESOLVES (by type
                  │ reflect.Call         │ reflect.Call         │ structure)
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
```

**When to use**:
- Default choice for most codebases
- When concrete types are wired via constructors in visible code
- When precision matters more than coverage

**Limitation**: Cannot trace through `reflect.Call`, `interface{}`/`any` type assertions at runtime, or DI containers that use reflection.

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
- Reflection-based DI frameworks (dig, fx, wire)
- When you want the broadest possible coverage
- When `--algo vta` misses calls because concrete types aren't visibly wired

**Trade-off**: Over-approximates. CHA reports `Store.Get` being dispatched to `MockStore.Get` even if `MockStore` is never used at runtime. trawl mitigates this with filters:

### CHA False-Positive Filters

```
Filter                      │ What it catches                         │ How
────────────────────────────┼─────────────────────────────────────────┼────────────────────
Ubiquitous interface filter │ error.Error(), fmt.Stringer.String()    │ Skip dispatch on
                            │ io.Reader.Read(), context.Context, etc. │ known noisy interfaces
Mock type filter            │ (*MockStore).Get(), (*MockClient).Do()  │ Skip types with
                            │                                         │ "Mock" name prefix
Interface method labeling   │ Shows Store.Get not MockStore.Get       │ interfaceMethodLabel()
Cross-module inference      │ Wrapper pkgs (rediscache → go-redis)    │ 2-level import scan
```

## `--scope` Flag

`--scope` loads additional packages into the SSA program to enrich the type universe. The primary package (`--pkg`) remains the analysis target.

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
Handler with dig/fx/wire DI                 │ --algo cha --scope ./...
Maximum coverage, accept false positives    │ --algo cha --scope ./...
Fast analysis, good-enough precision        │ --algo rta
Analyzing a leaf package in isolation       │ --algo vta (no external calls expected)
Leaf package + want to see injected deps    │ --algo vta --scope ./path/to/wiring
```
