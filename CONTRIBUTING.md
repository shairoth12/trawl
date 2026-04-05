# Contributing to trawl

## Development setup

Requirements: Go 1.25+

```bash
git clone https://github.com/shairoth12/trawl
cd trawl
make build   # produces bin/trawl
make test    # go test -v -race ./...
make lint    # golangci-lint run ./...
```

## Commit messages

All commits must follow [Conventional Commits v1.0.0](https://www.conventionalcommits.org/):

```
<type>[optional scope]: <description>
```

Common types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `ci`

Examples:
- `feat(cli): add --json-compact output flag`
- `fix: handle missing go.sum file gracefully`
- `test(walker): add cycle detection coverage`

Commit messages drive the automated changelog, so use the correct type.

## Pull requests

1. Fork the repo and create a branch from `main`.
2. Add or update tests for any logic you change.
3. Run `make all` (lint + test + build) before opening a PR.
4. Keep PRs focused — one logical change per PR.

## Release process

Releases are cut by maintainers. To validate the release pipeline locally:

```bash
make release-dry-run   # requires goreleaser installed
```

This runs a snapshot build for all target platforms without publishing anything.
