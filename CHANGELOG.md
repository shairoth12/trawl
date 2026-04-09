# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
