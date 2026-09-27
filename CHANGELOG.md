# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `--deps auto|none` flag (default `auto`): SSA function bodies are built for
  same-module packages and for dependency packages that implement interfaces the
  analyzed code invokes (≤3 selection rounds, ≤200 packages, never stdlib or
  indicator packages)
- `resolved_via: cross_module_trace` — a dependency body walked to a backend
  call, attributed to the module-side call site
- `resolved_via: interface_dispatch` — interface calls with no concrete callee,
  classified by the interface's declaring package (high when it is an
  indicator, low when inferred from imports)
- Stats fields `packages_analyzed`, `dependency_packages`, `unresolved_invokes`
- Two-module fixture `testdata/crossmodule` (analyzed module + dependency module)

### Changed

- Records describing one call site (same service type, same position) are
  merged; higher confidence wins, ties keep the interface label
- Generic top-level callee edges are no longer dropped (package recovered via
  `Origin()`); external generic implementors report the concrete SSA name
- `io.ReadCloser`, `io.WriteCloser`, `io.ReadWriteCloser` join the ubiquitous
  interface filter
- `analysis.Load` takes an `Options` struct; `walker.New` takes `walker.Options`

### Fixed

- Interface calls whose implementation lives in a dependency module resolve
  without adding the dependency to `--scope`
- SSA build panics in dependency packages surface as errors instead of crashing

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

[Unreleased]: https://github.com/shairoth12/trawl/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/shairoth12/trawl/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/shairoth12/trawl/releases/tag/v0.1.0
