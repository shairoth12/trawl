# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/shairoth12/trawl/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/shairoth12/trawl/releases/tag/v0.1.0
