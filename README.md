# trawl

[![Latest Release](https://img.shields.io/github/v/release/shairoth12/trawl)](https://github.com/shairoth12/trawl/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/shairoth12/trawl)](https://go.dev/doc/install)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Static analysis CLI for Go. Walks the call graph from an entry-point function and reports every external service call reachable from it. Output is JSON.

```
  Source code ──► SSA IR ──► Call graph (VTA/RTA/CHA) ──► DFS walk ──► JSON
```

trawl detects calls to [10 built-in service types](#built-in-indicators) — HTTP, gRPC, Redis, Postgres, Pub/Sub, Vault, and more — out of the box. Custom service types are declared via a YAML config file. Designed for automation pipelines and LLM-based tooling.

## Prerequisites

- Go 1.25 or later
- Target package must be loadable by `go/packages` (must compile, dependencies available)
- The Go version used to build trawl must be >= the Go version required by the target module

## Installation

```bash
go install github.com/shairoth12/trawl/cmd/trawl@latest
```

Or build from source:

```bash
git clone https://github.com/shairoth12/trawl.git
cd trawl
go build -o trawl ./cmd/trawl
```

## Quick Start

```bash
trawl --pkg ./cmd/server --entry HandleRequest
```

```json
{
  "entry_point": "github.com/example/myapp/cmd/server.HandleRequest",
  "package": "github.com/example/myapp/cmd/server",
  "external_calls": [
    {
      "service_type": "HTTP",
      "import_path": "net/http",
      "function": "(*net/http.Client).Do",
      "short_function": "(*Client).Do",
      "file": "cmd/server/handler.go",
      "line": 42,
      "call_chain": [
        "github.com/example/myapp/cmd/server.HandleRequest",
        "github.com/example/myapp/internal/client.(*HTTPClient).Fetch",
        "(*net/http.Client).Do"
      ],
      "short_call_chain": ["HandleRequest", "(*HTTPClient).Fetch", "(*Client).Do"],
      "resolved_via": "direct",
      "confidence": "high"
    },
    {
      "service_type": "REDIS",
      "import_path": "github.com/redis/go-redis/v9",
      "function": "(*github.com/redis/go-redis/v9.Client).Get",
      "short_function": "(*Client).Get",
      "file": "cmd/server/handler.go",
      "line": 57,
      "call_chain": [
        "github.com/example/myapp/cmd/server.HandleRequest",
        "github.com/example/myapp/internal/cache.(*Store).Lookup",
        "(*github.com/redis/go-redis/v9.Client).Get"
      ],
      "short_call_chain": ["HandleRequest", "(*Store).Lookup", "(*Client).Get"],
      "resolved_via": "direct",
      "confidence": "high"
    }
  ]
}
```

## Usage

```
trawl --pkg <pattern> --entry <name> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--pkg` | `.` | Go package load pattern (e.g. `.`, `./cmd/server`) |
| `--entry` | _(required)_ | Entry point function name |
| `--config` | _(none)_ | Path to YAML config file for custom service indicators |
| `--algo` | `vta` | Call graph algorithm: `vta`, `rta`, or `cha` |
| `--scope` | _(none)_ | Extra package patterns for type visibility (comma-separated) |
| `--dedup` | _(off)_ | Deduplicate by `(service_type, import_path, function)`, shortest chain wins |
| `--stats` | _(off)_ | Append analysis diagnostics to JSON output (package count, call graph size, DFS counters, phase durations) |
| `--timeout` | `10m` | Maximum analysis duration; `0` disables |
| `--log-level` | `info` | Log verbosity: `off`, `error`, `warn`, `info`, or `debug` |
| `--log-file` | _(stderr)_ | Write logs to a file instead of stderr |
| `--log-format` | `text` | Log format: `text` or `json` |
| `--version` | | Print version and exit |

Stage-progress logs are written to stderr at `info` level by default. Use `--log-file` to redirect them so that stderr contains only true errors. Use `--log-level off` for stdout-only JSON output.

Responds to `SIGINT`/`SIGTERM` for clean abort. Exit code is non-zero on error.

### Entry Point Formats

| Format | Example | Resolves to |
|--------|---------|-------------|
| Top-level function | `HandleRequest` | Package-level function |
| Qualified method | `Handler.ServeHTTP` | Method on type `Handler` (pointer or value receiver) |
| Bare method name | `ServeHTTP` | Unique method across all types; error if ambiguous |

### Examples

```bash
# Basic analysis
trawl --pkg ./cmd/server --entry HandleRequest

# Method on a named type
trawl --pkg ./internal/handler --entry Handler.ServeHTTP

# Custom indicators
trawl --pkg ./cmd/worker --entry ProcessJob --config trawl.yaml

# RTA (faster, less precise for interfaces)
trawl --pkg ./cmd/server --entry HandleRequest --algo rta

# CHA for reflection-based DI (dig, fx, wire)
trawl --pkg ./internal/handler --entry Handle --scope ./... --algo cha

# VTA with scope for manual constructor DI
trawl --pkg ./internal/handler --entry Handle --scope ./cmd/server

# Deduplicate and count
trawl --pkg ./cmd/server --entry HandleRequest --dedup | jq '.external_calls | length'

# Diagnose slow analysis (packages loaded, call graph size, phase timing)
trawl --pkg ./cmd/server --entry HandleRequest --stats --log-level off | jq .stats

# Extract unique service types
trawl --pkg ./cmd/server --entry HandleRequest | jq '[.external_calls[].service_type] | unique'

# Suppress stage logs (stdout JSON only)
trawl --pkg ./cmd/server --entry HandleRequest --log-level off

# Write logs to a file, keep stderr clean for errors
trawl --pkg ./cmd/server --entry HandleRequest --log-file trawl.log

# Debug per-edge decisions
trawl --pkg ./cmd/server --entry HandleRequest --log-level debug

# JSON logs for machine consumption
trawl --pkg ./cmd/server --entry HandleRequest --log-format json --log-file trawl.log
```

## How It Works

```
Stage 1: go/packages.Load()
         Load target package (+ scope packages) into typed AST
              │
Stage 2: ssautil.Packages() + prog.Build()
         Convert to SSA intermediate representation
              │
Stage 3: Construct call graph
         ├─ VTA: CHA seed → vta.CallGraph (default, most precise)
         ├─ RTA: deferred to after entry resolution
         └─ CHA: cha.CallGraph (broadest, handles reflection DI)
              │
Stage 4: Resolve entry point
         "FuncName" | "Type.Method" | "BareMethod" → *ssa.Function
              │
Stage 5: DFS Walker
         For each callee edge from current node:
         ┌─ Detector match? → emit ExternalCall (direct, high)
         ├─ Mock type? → infer from imports or skip
         ├─ Ubiquitous interface? (error, io.Reader) → skip
         ├─ Outside module? → infer from transitive imports or stop
         └─ Inside module? → recurse
              │
Stage 6: Post-process
         ├─ Relativize file paths
         ├─ Deduplicate (--dedup)
         └─ Populate short_function / short_call_chain
              │
Stage 7: JSON → stdout
```

## Call Graph Algorithms

| Algorithm | Precision | Speed | Interface Resolution | Handles Reflection DI |
|-----------|-----------|-------|---------------------|-----------------------|
| **VTA** (default) | High | Slower | By observed value flow | No |
| **RTA** | Medium | Faster | By instantiated types | No |
| **CHA** | Low (with filters) | Fastest | By structural type match | Yes |

**Use VTA** when concrete types are wired through visible constructors.
**Use CHA + `--scope ./...`** when using reflection-based DI (dig, fx, wire).

See [docs/ALGORITHMS.md](docs/ALGORITHMS.md) for the full decision guide.

## Configuration File

Optional YAML. Declares custom service indicators and wrapper libraries.

```yaml
indicators:
  # Override a built-in
  - package: "database/sql"
    service_type: "MYSQL"

  # Custom service type
  - package: "github.com/your-org/bolt-client"
    service_type: "BOLT"

  # Wrapper library: both paths classified as REDIS, direct/high confidence
  - package: "github.com/your-org/rediscache"
    service_type: "REDIS"
    wrapper_for:
      - "github.com/custom-redis/client"
```

User indicators take precedence over built-ins. First matching prefix wins.

See [docs/CONFIGURATION.md](docs/CONFIGURATION.md) for the full reference.

### Built-in Indicators

| Service Type | Matched Import Path Prefixes |
|--------------|------------------------------|
| `HTTP` | `net/http` |
| `GRPC` | `google.golang.org/grpc` |
| `REDIS` | `github.com/go-redis/redis`, `github.com/redis/go-redis` |
| `PUBSUB` | `cloud.google.com/go/pubsub` |
| `DATASTORE` | `cloud.google.com/go/datastore` |
| `FIRESTORE` | `cloud.google.com/go/firestore` |
| `POSTGRES` | `database/sql`, `github.com/lib/pq`, `github.com/jackc/pgx` |
| `ELASTICSEARCH` | `github.com/elastic/go-elasticsearch` |
| `VAULT` | `github.com/hashicorp/vault/api` |
| `ETCD` | `go.etcd.io/etcd/client` |

## Output Format

Single JSON object to stdout. The `external_calls` array is never null.

When `--stats` is passed, a `stats` object is appended with diagnostic measurements for the run:

```json
"stats": {
  "packages_loaded": 1911,
  "call_graph_nodes": 64320,
  "call_graph_edges": 26548,
  "nodes_visited": 60,
  "edges_examined": 430,
  "load_duration_ms": 2177,
  "walk_duration_ms": 0
}
```

`packages_loaded` and `load_duration_ms` are the primary signals for slow analyses. `nodes_visited` vs `call_graph_nodes` shows what fraction of the graph the DFS actually traversed. `walk_duration_ms` is sub-millisecond for most analyses (reported as `0`).

Each call includes:
- `service_type`, `import_path`, `function`, `file`, `line`
- `call_chain` — ordered path from entry to call site
- `resolved_via` — `"direct"`, `"mock_inference"`, or `"cross_module_inference"`
- `confidence` — `"high"`, `"medium"`, or `"low"`
- `short_function`, `short_call_chain` — paths and generics stripped

See [docs/OUTPUT-FORMAT.md](docs/OUTPUT-FORMAT.md) for the full schema reference.

## Dependency Injection

When analyzing leaf packages whose interfaces are implemented elsewhere:

```bash
# Manual DI (constructor injection) — VTA traces value flow
trawl --pkg ./internal/handler --entry Handle --scope ./cmd/server --algo vta

# Reflection-based DI (dig, fx) — CHA resolves by type structure
trawl --pkg ./internal/handler --entry Handle --scope ./... --algo cha
```

## Documentation

| Document | Description |
|----------|-------------|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System overview, pipeline, package map, design decisions |
| [docs/INTERNALS.md](docs/INTERNALS.md) | Deep dive into each internal package's API and logic |
| [docs/ALGORITHMS.md](docs/ALGORITHMS.md) | VTA vs RTA vs CHA decision guide |
| [docs/CONFIGURATION.md](docs/CONFIGURATION.md) | Config file format, indicator system, examples |
| [docs/OUTPUT-FORMAT.md](docs/OUTPUT-FORMAT.md) | JSON output schema reference |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Development setup, commit conventions, PR workflow |
| [CHANGELOG.md](CHANGELOG.md) | Release history |

## License

MIT — see [LICENSE](LICENSE)
