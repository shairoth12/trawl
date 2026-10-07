# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `trawl --version` printed `dev` for binaries installed with
  `go install github.com/shairoth12/trawl/cmd/trawl@vX.Y.Z`. It now prints the
  module version.

## [0.3.0] - 2026-10-07

### Added

- `--deps auto|none` flag (default `auto`): function bodies are also built for
  other packages of your module and for dependency packages that implement an
  interface your code calls (≤3 rounds, ≤200 packages, never stdlib or
  indicator packages), so such calls resolve without `--scope`
- `resolved_via: cross_module_trace` — trawl followed a call into a dependency
  and found a backend call there; the record points at the call in your code
- `resolved_via: interface_dispatch` — an interface call that resolved to no
  implementation; classified by the package that declares the interface (high
  when that package is an indicator, low when guessed from its imports)
- Consumers that check `resolved_via` against a fixed list must accept
  `cross_module_trace` and `interface_dispatch`
- Stats fields `packages_analyzed`, `dependency_packages`, `unresolved_invokes`
- Two-module fixture `testdata/crossmodule` (analyzed module + dependency module)
- "Compatibility" section in `docs/OUTPUT-FORMAT.md`: what may change in a minor
  release. `service_type` and `resolved_via` are open enums (accept unknown
  values); `confidence` is closed (`high`, `medium`, `low`)

### Changed

- VTA: an interface call that VTA resolves to nothing, or only to mocks
  (typical for dig/fx injection), gets the CHA callees instead, so the walk enters the
  implementation and its dependency body; interfaces declared in the standard
  library are not filled, and callees without a built body (e.g. same-module
  packages under `--deps none`) are not copied, so the call stays unresolved
  instead of vanishing. Under VTA such calls move from `interface_dispatch`
  to `cross_module_trace`, and `unresolved_invokes` drops
- Several hits for the same line and service type are merged into one record;
  higher confidence wins, ties keep the interface name
- Calls to generic top-level functions (e.g. `Map[T, U]`) are no longer dropped;
  external generic types now report their concrete method name
- A call to a generic function or method in an indicator package is reported as
  `direct` / high, like any other call into that package; existing output can
  gain such records
- A type counts as a mock when it is a struct with a field of type `mock.Mock`
  (testify, mockery) or `*gomock.Controller` (mockgen), not when its name starts
  with `Mock`. A real type like `MockingbirdClient` is now walked, and so are
  hand-written mocks without such a field (e.g. `type MockStore struct{}`).
  Applies to the walk, dependency selection and bare-method entry points
- The toolchain warning appears only when the host `go` is a newer release
  (e.g. 1.27 vs 1.26) than the one trawl was built with; an older host or a
  different patch release no longer warns
- `io.ReadCloser`, `io.WriteCloser`, `io.ReadWriteCloser` join the list of very
  common interfaces that are ignored
- `analysis.Load` takes an `Options` struct; `walker.New` takes `walker.Options`
- Standard-library packages are recognized by the loader's module data, not by
  the path alone, so a module whose path has no dot (`module svc`) is treated
  as your code; in GOPATH mode the path rule still decides
- Go API: `ExternalCall.ResolvedVia` and `ExternalCall.Confidence` have the new
  named types `trawl.ResolvedVia` and `trawl.Confidence`, like `ServiceType`.
  The JSON output is unchanged

### Removed

- Go API: the root package now holds types only. `ShortenName`, `NewResult`,
  `LoadConfig` and `Config.Validate` are no longer exported. The CLI and its
  output are not affected

### Fixed

- Interface calls whose implementation lives in a dependency module resolve
  without adding the dependency to `--scope`
- SSA build panics in dependency packages surface as errors instead of crashing
- `--timeout` also stops the SSA build: no new package builds start after it
  expires, and trawl returns once the running ones finish
- A package whose path only starts with the module path (`example.com/app-extra`
  for module `example.com/app`) is no longer treated as part of the module
- `--dedup` with no external calls printed `"external_calls": null`; it now
  prints `[]`

## [0.2.0] - 2026-04-10

### Added

- `--stats` flag: appends a `stats` block to JSON output with timing breakdowns
  (`load_duration_ms`, `analysis_duration_ms`, `total_duration_ms`) and package
  counts (`packages_loaded`, `packages_analyzed`)
- `--log-level` flag (`debug`, `info`, `warn`, `error`, `off`; default: `off`) for
  structured logging via `log/slog` — stderr stays silent unless opted in
- `--log-format` flag (`text` or `json`) to control log output format
- `--log-file` flag to redirect logs to a file instead of stderr
- LLM agent skills: `skills/trawl/SKILL.md` and `skills/trawl-config/SKILL.md`
  for driving trawl analysis and config generation from Claude Code or any
  skills-aware agent

### Documentation

- README slimmed; detailed reference extracted to `docs/` (`ARCHITECTURE.md`,
  `ALGORITHMS.md`, `CONFIGURATION.md`, `INTERNALS.md`, `OUTPUT-FORMAT.md`)

## [0.1.0] - 2026-03-30

### Added

- CLI with configurable call-graph algorithm: VTA (default), RTA, and CHA
- Built-in detection for 10 external service types: HTTP, gRPC, Redis, Pub/Sub,
  Datastore, Firestore, Postgres, Elasticsearch, Vault, and etcd
- YAML config file support for custom service indicators and wrapper annotations
- `--dedup` flag to remove duplicate results, keeping the shortest call chain
- `--scope` flag to supply extra packages for improved type-visibility under VTA
- `--timeout` flag with a default of 10 minutes
- JSON output including call chain, confidence, `resolved_via`, and file/line info
- `--version` flag reporting the binary version and compile-time Go version
- Runtime warning when the binary's Go version differs from the host toolchain
  (trawl uses `go/packages` which invokes the host `go` command)
- `ShortenName` exported helper for compacting SSA qualified names

[Unreleased]: https://github.com/shairoth12/trawl/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/shairoth12/trawl/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/shairoth12/trawl/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/shairoth12/trawl/releases/tag/v0.1.0
