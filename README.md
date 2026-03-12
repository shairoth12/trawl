# trawl

trawl is a Go static analysis CLI that walks the call graph of a Go package and
reports every external service call reachable from a given entry point function.
Given a package pattern and a function name, trawl loads the package into SSA
form, constructs a full call graph (using VTA or RTA), and performs a
depth-first traversal from the entry point — stopping at module boundaries and
matching package import paths against a built-in set of service indicators
(HTTP, gRPC, Redis, Postgres, Vault, and more) as well as any user-supplied
indicators from a YAML config file. Results are written to stdout as JSON,
making trawl suitable for automation pipelines and LLM-based tooling.

## Prerequisites

- Go 1.24 or later
- The target package must be loadable by `go/packages` (i.e. it must compile
  and its dependencies must be present in the module cache or vendor directory)
- The Go version used to build trawl must be greater than or equal to the Go
  version required by the target module. If you see a "toolchain version
  mismatch" error, rebuild trawl with the current toolchain:
  `go build -o trawl ./cmd/trawl`

## Installation

Install the latest release directly from source:

```bash
go install github.com/shairoth12/trawl/cmd/trawl@latest
```

To build from source after cloning the repository:

```bash
git clone https://github.com/shairoth12/trawl.git
cd trawl
go build -o trawl ./cmd/trawl
```

## Quick start

Analyze the `HandleRequest` function in the package at `./cmd/server` using the
default VTA call graph algorithm:

```bash
trawl --pkg ./cmd/server --entry HandleRequest
```

Sample output:

```json
{
  "entry_point": "github.com/example/myapp/cmd/server.HandleRequest",
  "package": "github.com/example/myapp/cmd/server",
  "external_calls": [
    {
      "service_type": "HTTP",
      "import_path": "net/http",
      "function": "(*Client).Do",
      "file": "/home/user/myapp/cmd/server/handler.go",
      "line": 42,
      "call_chain": [
        "github.com/example/myapp/cmd/server.HandleRequest",
        "github.com/example/myapp/internal/client.(*HTTPClient).Fetch",
        "net/http.(*Client).Do"
      ]
    },
    {
      "service_type": "REDIS",
      "import_path": "github.com/redis/go-redis/v9",
      "function": "(*Client).Get",
      "file": "/home/user/myapp/cmd/server/handler.go",
      "line": 57,
      "call_chain": [
        "github.com/example/myapp/cmd/server.HandleRequest",
        "github.com/example/myapp/internal/cache.(*Store).Lookup",
        "github.com/redis/go-redis/v9.(*Client).Get"
      ]
    }
  ]
}
```

## Usage

```
trawl --pkg <pattern> --entry <name> [--config <yaml>] [--algo vta|rta]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--pkg` | `.` | Go package load pattern (e.g. `.`, `./cmd/server`, `github.com/foo/bar`) |
| `--entry` | _(required)_ | Entry point function name. Accepts `FunctionName`, `Type.Method`, or a bare method name when unambiguous |
| `--config` | _(none)_ | Path to a YAML config file containing custom service indicators |
| `--algo` | `vta` | Call graph algorithm: `vta` (Variable Type Analysis, default) or `rta` (Rapid Type Analysis) |

trawl responds to `SIGINT` and `SIGTERM` and will abort the analysis cleanly.
Exit code is non-zero on any error; the error message is written to stderr.

### Entry point formats

| Format | Example | Resolves to |
|--------|---------|-------------|
| Top-level function | `HandleRequest` | Package-level function with that name |
| Qualified method | `Handler.ServeHTTP` | Method `ServeHTTP` on type `Handler` (value or pointer receiver) |
| Bare method name | `ServeHTTP` | Unique method across all types in the package; error if ambiguous |

### Examples

```bash
# Analyze a top-level function in the current module
trawl --pkg ./cmd/server --entry HandleRequest

# Analyze a method on a named receiver type
trawl --pkg ./internal/handler --entry Handler.ServeHTTP

# Use a config file with custom service indicators
trawl --pkg ./cmd/worker --entry ProcessJob --config trawl.yaml

# Use RTA instead of VTA (faster; less precise for interface dispatch)
trawl --pkg ./cmd/server --entry HandleRequest --algo rta

# Analyze a package by full import path
trawl --pkg github.com/example/myapp/cmd/server --entry HandleRequest

# Pipe the output into jq to extract service types
trawl --pkg ./cmd/server --entry HandleRequest | jq '[.external_calls[].service_type] | unique'
```

## Configuration file

The config file is optional YAML. It lets you declare additional package import
path prefixes that should be treated as a named service type. User-supplied
indicators take precedence over built-in ones; the first matching prefix wins.

```yaml
indicators:
  # Override the built-in POSTGRES label for database/sql with a custom one.
  - package: "database/sql"
    service_type: "MYSQL"

  # Classify an internal wrapper library as a known service type.
  - package: "github.com/your-org/infra/pubsub"
    service_type: "PUBSUB"

  # Introduce a completely custom service type label.
  - package: "github.com/your-org/bolt-client"
    service_type: "BOLT"
```

Each entry has two fields:

| Field | Type | Description |
|-------|------|-------------|
| `package` | string | Import path prefix to match (prefix match, not exact) |
| `service_type` | string | Label assigned to calls whose import path starts with `package` |

Pass the file to trawl with:

```bash
trawl --pkg ./cmd/server --entry HandleRequest --config trawl.yaml
```

### Built-in indicators

The following indicators are active by default when no config is provided (or in
addition to config indicators, with lower precedence):

| Service type | Matched import path prefixes |
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

## How it works

```
  go/packages ──► go/ssa (SSA IR) ──► CHA seed ──► VTA call graph
                                              or: RTA (built from entry point)
                                                        │
                                             Resolve entry *ssa.Function
                                          (top-level / Type.Method / bare)
                                                        │
                                                        ▼
                                         ┌──────────────────────────────┐
                                         │          DFS Walker          │
                                         │  for each callee:            │
                                         │  1. indicator match?  ───────┼──► record ExternalCall
                                         │  2. out-of-module?    ───────┼──► stop
                                         │  3. already visited?  ───────┼──► stop
                                         │  4. recurse                  │
                                         └──────────────────────────────┘
                                                        │
                                                        ▼
                                                 JSON ──► stdout
```

Indicator matching uses prefix comparison against the merged list (user config
first, then built-in). The detector runs before the module-boundary check so
calls into third-party packages are always captured, never silently skipped.

## Output format

trawl writes a single JSON object to stdout. The object always contains the
three top-level keys below, even when no external calls are found.

```json
{
  "entry_point": "string",
  "package":     "string",
  "external_calls": [ ... ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `entry_point` | string | Fully-qualified SSA name of the resolved entry point function |
| `package` | string | Import path of the directly-analyzed package |
| `external_calls` | array | Ordered list of detected external service calls (may be empty, never null) |

Each element of `external_calls`:

| Field | Type | Description |
|-------|------|-------------|
| `service_type` | string | Matched service label (e.g. `"HTTP"`, `"REDIS"`, or a custom label) |
| `import_path` | string | Full Go import path of the package where the call was detected |
| `function` | string | Function or method name within that package |
| `file` | string | Absolute path to the source file containing the call site |
| `line` | integer | Line number of the call site; `0` for synthetic call graph edges |
| `call_chain` | array of strings | Ordered sequence of fully-qualified function names from the entry point to the detected call |

## License

MIT — see LICENSE
