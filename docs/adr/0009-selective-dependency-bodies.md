---
status: accepted
---

# Only some dependency packages get function bodies

## Context

`ssautil.Packages` builds function bodies only for the packages named by `--pkg` and `--scope`. Every other package is loaded as signatures only. A method implemented in a dependency module therefore has no body, never enters the call graph, and an interface call into it reports nothing unless the user adds that module to `--scope`.

## Decision

`analysis.Load` builds bodies for the `--pkg`/`--scope` packages, plus every other package of the analyzed module, plus dependency packages that contain a concrete type (not a mock, see `trawl.IsMock`) implementing an interface the analyzed code calls. Selection works on type information only (`TypesInfo.Selections`), no SSA needed. It runs up to 3 rounds so that a facade which itself calls a second interface gets that implementation built too, and stops at 200 dependency-module packages. Standard-library packages and indicator packages are never selected.

## Considered options

- **Build bodies for every loaded package.** Too expensive: a real service loads around 2000 packages. Rejected.
- **Build a first program, find what is missing, then build more.** Not possible with go/ssa: `CreatePackage` may be called only once per package, so the full list must be known before the program is created.
- **Keep `--scope` as the only way.** Works, but pushes every user toward `--scope ./...`, and `--scope` cannot reach packages in other modules at all.

## Consequences

The two numbers are guesses, not measured. Three rounds covers the common case of two interface hops (your code → facade → client wrapper) with one round to spare; longer chains end in `interface_dispatch` records instead of a traced backend. The 200 cap counts packages, not their size, and exists only as a safety limit for the rare case where a very generic interface (say `Get(ctx, key)`) matches dozens of unrelated packages. Both should be checked against `stats.dependency_packages` and `load_duration_ms` on a real target. The cap is already an `analysis.Options` field (`MaxDependencyPkgs`), so exposing it as a flag is a two-line change.
