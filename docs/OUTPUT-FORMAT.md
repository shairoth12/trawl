# Output Format

trawl writes a single JSON object to stdout. This document is the authoritative schema reference.
Go programs can decode it into `trawl.Result` from the root package.

## Compatibility

trawl follows [semantic versioning](https://semver.org/). For the JSON output that means:

```
Stable within a major version      │ May change in a minor release
───────────────────────────────────┼──────────────────────────────────────────────
Field names and their JSON types   │ New fields (ignore fields you don't know)
Which fields are always present    │ New values of the open enums:
external_calls is never null       │   service_type, resolved_via
The closed enum: confidence        │
```

- **Open enums** (`service_type`, `resolved_via`): new values may appear in any release.
  Don't fail on a value you don't know. For an unknown `resolved_via`, go by `confidence`.
- **Closed enum** (`confidence`): always `high`, `medium` or `low`. A new level would be a
  major version.
- **Not part of the contract**: which calls are found, the contents of `call_chain`, the
  formatting of `function` and `short_*` names, and all `stats` values. These are analysis
  results; a better analysis changes them in a minor release, and the CHANGELOG says how.

## Top-Level Object

```json
{
  "entry_point":    "string",
  "package":        "string",
  "external_calls": [ ... ],
  "deduplicated":   true,
  "stats":          { ... }
}
```

```
Field          │ Type     │ Always present │ Description
───────────────┼──────────┼────────────────┼─────────────────────────────────────────────
entry_point    │ string   │ YES            │ Fully-qualified SSA name of resolved entry point
package        │ string   │ YES            │ Import path of the directly-analyzed package
external_calls │ array    │ YES            │ List of ExternalCall objects. Never null, may be empty.
deduplicated   │ boolean  │ NO             │ Present and true only when --dedup was used
stats          │ object   │ NO             │ Present only when --stats is passed; see AnalysisStats below
```

## AnalysisStats Object

Present in the top-level output only when `--stats` is passed.

```json
{
  "packages_loaded": 1911,
  "packages_analyzed": 14,
  "dependency_packages": 12,
  "call_graph_nodes": 64320,
  "call_graph_edges": 26548,
  "nodes_visited": 60,
  "edges_examined": 430,
  "unresolved_invokes": 1,
  "load_duration_ms": 2177,
  "walk_duration_ms": 0
}
```

```
Field             │ Type    │ Description
──────────────────┼─────────┼───────────────────────────────────────────────────────────────────
packages_loaded     │ integer │ Total packages transitively loaded by the go toolchain (packages.Visit count)
packages_analyzed   │ integer │ Packages whose function bodies were built (--pkg/--scope packages + dependency_packages)
dependency_packages │ integer │ Extra packages trawl chose to build bodies for: other packages of your module, and dependency packages implementing an interface your code calls
call_graph_nodes    │ integer │ Total functions (nodes) in the constructed call graph
call_graph_edges    │ integer │ Total call sites (edges) across all nodes in the call graph
nodes_visited       │ integer │ Functions entered during the walk; a dependency function is counted again each time it is reached from a different call in your code
edges_examined      │ integer │ Total outgoing edges considered during DFS (including skipped)
unresolved_invokes  │ integer │ Interface calls that resolved to no implementation, or only to mocks (standard-library and very common interfaces are not counted)
load_duration_ms  │ integer │ Wall-clock milliseconds from package load start through call graph construction (all algorithms)
walk_duration_ms  │ integer │ Wall-clock milliseconds spent in the DFS walk; typically 0 for most analyses
```

**Interpreting stats:**

- `packages_loaded` and `load_duration_ms` are the primary indicators of analysis cost. Load time scales with package count, not call graph size.
- `load_duration_ms` is consistent across all algorithms: it covers package load + SSA build + call graph construction. For VTA/CHA the call graph is built inside `analysis.Load`; for RTA it is built by `rta.Analyze` immediately after, so both phases are included in the timer.
- `nodes_visited / call_graph_nodes` shows coverage: for a deep entry point this ratio approaches 1; for a shallow handler it is typically < 1%.
- `walk_duration_ms` reports 0 for most analyses because the DFS completes in sub-millisecond time. This is expected and correct.
- `call_graph_edges` counts all edges in the full graph (constructed once). `edges_examined` counts only edges actually traversed from the given entry point.
- `unresolved_invokes > 0` means some interface call could not be resolved to a real implementation. `dependency_packages` shows how many extra packages trawl built to try; compare with `--deps none` to see what they contributed.

## ExternalCall Object

```json
{
  "service_type":     "REDIS",
  "import_path":      "github.com/redis/go-redis/v9",
  "function":         "(*github.com/redis/go-redis/v9.Client).Get",
  "file":             "internal/cache/store.go",
  "line":             42,
  "call_chain":       ["pkg.HandleRequest", "pkg.(*Store).Lookup", "(*go-redis.Client).Get"],
  "resolved_via":     "direct",
  "confidence":       "high",
  "short_function":   "(*Client).Get",
  "short_call_chain": ["HandleRequest", "(*Store).Lookup", "(*Client).Get"]
}
```

```
Field            │ Type          │ Always │ Description
─────────────────┼───────────────┼────────┼──────────────────────────────────────────────────
service_type     │ string        │ YES    │ Matched service label (e.g. "HTTP", "REDIS", custom)
import_path      │ string        │ YES    │ Go import path of the package where call was detected
function         │ string        │ YES    │ Fully-qualified SSA function/method name
file             │ string        │ YES    │ Relative path to source file; "" for synthetic edges
line             │ integer       │ YES    │ Source line number; 0 for synthetic edges
call_chain       │ string[]      │ YES    │ Ordered: entry → intermediates → call site
resolved_via     │ string        │ YES    │ Detection method (see enum below)
confidence       │ string        │ YES    │ Reliability grade (see enum below)
short_function   │ string        │ YES    │ function with module paths + generics stripped
short_call_chain │ string[]      │ YES    │ call_chain with same stripping applied per entry
```

## Enums

### `resolved_via` (open)

```
Value                      │ Meaning                                          │ Typical confidence
───────────────────────────┼──────────────────────────────────────────────────┼───────────────────
direct                     │ Import path matched an indicator prefix          │ high
                           │ (including wrapper_for expansions)               │
mock_inference             │ Mock type detected; service type inferred from   │ medium
                           │ the mock's package imports                       │
cross_module_inference     │ External module; service type inferred from      │ low
                           │ 2-level transitive imports                       │
cross_module_trace         │ trawl followed the call into a dependency and    │ high (low if the backend
                           │ found a backend call; reported at your call      │ inside was itself guessed)
interface_dispatch         │ Interface call with no known implementation;     │ high when the interface's
                           │ classified by the package declaring the interface│ package is an indicator,
                           │                                                  │ low when guessed from imports
```

For `cross_module_trace` records, `file`/`line`/`function`/`import_path` describe **your call** that led into the dependency, while `call_chain` continues into the dependency down to the backend call:

```json
{
  "service_type": "POSTGRES",
  "resolved_via": "cross_module_trace",
  "confidence": "high",
  "file": "handler.go", "line": 12,                          ← your call: h.store.Get(ctx, key)
  "function": "(*example.com/lib/store.sqlStore).Get",       ← the dependency method it entered
  "call_chain": ["svc.HandleGet", "(*store.sqlStore).Get", "(*database/sql.DB).QueryRowContext"]
}
```

When confidence is `low` (`interface_dispatch`, `cross_module_inference`, or a `cross_module_trace` whose inner hit was itself guessed), `service_type` comes from the **first** recognised import of the package that declares the interface. For a facade wrapping several backends it may name the wrong one — read a `low` record as "some external call here", not as the service identity. With `--deps auto` and a visible implementor the record is `high` and classified per method.

### `confidence` (closed)

```
Value   │ Meaning
────────┼───────────────────────────────────────────────────────────────
high    │ Direct indicator match. Reliable.
medium  │ Mock inference. Likely correct, depends on import conventions.
low     │ Transitive import inference. Treat as a hint, verify manually.
```

### `service_type` (open; built-in values)

```
HTTP, GRPC, REDIS, PUBSUB, DATASTORE, FIRESTORE,
POSTGRES, ELASTICSEARCH, VAULT, ETCD
```

Custom values from config files appear as-is (e.g. `"KAFKA"`, `"MYSQL"`, `"BOLT"`).

## Mock Handling in Output

When a call is resolved through interface dispatch on a mock type, `function` and `call_chain` use the **interface method label** rather than the concrete mock type name:

```
Instead of:  "(*mockfilter.MockStore).Get"
Output:      "mockfilter.Store.Get"
```

This ensures downstream consumers (LLM agents, automation) see the abstract interface contract, not mock implementation details.

## Name Shortening

`short_function` and `short_call_chain` are computed by trawl as follows:

```
Input                                           │ Output
────────────────────────────────────────────────┼──────────────────
github.com/foo/bar.Get                          │ Get
(*github.com/foo/bar.Client).Do                 │ (*Client).Do
github.com/foo/bar.Cache[T].Set                 │ Cache.Set
(*github.com/foo/bar.Cache[K, V]).Get           │ (*Cache).Get
HandleRequest                                   │ HandleRequest
```

Algorithm:
1. Strip generic type parameters: remove all `[...]` blocks (handles nesting)
2. Strip import path prefix: find last `/`, find first `.` after it, drop everything between

## Merging (always on)

Merging is not a flag; it runs on every analysis. When trawl finds several hits for the
same line of your code with the same service type, it keeps one record. This happens
independently of `--dedup`, which is optional and runs afterwards on the merged result.

```
Merge key: (service_type, call-site position)

Higher confidence wins; on a tie the interface name ("Store.Get") beats a
concrete method name ("(*sqlStore).Get"); otherwise the first hit wins.
Hits without a source position are never merged.
```

This removes the concrete/mock duplicate CHA produces when a dependency package holds both the real implementation and a generated mock.

## Deduplication (`--dedup`)

When `--dedup` is passed:

```
Dedup key: (service_type, import_path, function)

For duplicates: keep the entry with the shortest call_chain.
Tie-breaking: first occurrence wins (stable insertion order).

Top-level "deduplicated": true is set in output.
```

## Complete Example

```bash
trawl --pkg ./cmd/server --entry HandleRequest --dedup
```

```json
{
  "entry_point": "github.com/example/myapp/cmd/server.HandleRequest",
  "package": "github.com/example/myapp/cmd/server",
  "deduplicated": true,
  "external_calls": [
    {
      "service_type": "HTTP",
      "import_path": "net/http",
      "function": "(*net/http.Client).Do",
      "file": "cmd/server/handler.go",
      "line": 42,
      "call_chain": [
        "github.com/example/myapp/cmd/server.HandleRequest",
        "(*net/http.Client).Do"
      ],
      "resolved_via": "direct",
      "confidence": "high",
      "short_function": "(*Client).Do",
      "short_call_chain": [
        "HandleRequest",
        "(*Client).Do"
      ]
    },
    {
      "service_type": "REDIS",
      "import_path": "github.com/redis/go-redis/v9",
      "function": "(*github.com/redis/go-redis/v9.Client).Get",
      "file": "cmd/server/handler.go",
      "line": 57,
      "call_chain": [
        "github.com/example/myapp/cmd/server.HandleRequest",
        "github.com/example/myapp/internal/cache.(*Store).Lookup",
        "(*github.com/redis/go-redis/v9.Client).Get"
      ],
      "resolved_via": "direct",
      "confidence": "high",
      "short_function": "(*Client).Get",
      "short_call_chain": [
        "HandleRequest",
        "(*Store).Lookup",
        "(*Client).Get"
      ]
    }
  ]
}
```

## Consuming with jq

```bash
# List unique service types
trawl --pkg ./cmd/server --entry Handle | jq '[.external_calls[].service_type] | unique'

# Count external calls per service type
trawl --pkg ./cmd/server --entry Handle | jq '[.external_calls[].service_type] | group_by(.) | map({type: .[0], count: length})'

# Extract short call chains only
trawl --pkg ./cmd/server --entry Handle | jq '.external_calls[] | {service_type, short_function, short_call_chain}'

# Filter to high-confidence only
trawl --pkg ./cmd/server --entry Handle | jq '[.external_calls[] | select(.confidence == "high")]'

# Get deduplicated import paths
trawl --pkg ./cmd/server --entry Handle --dedup | jq '[.external_calls[].import_path] | unique'

# Inspect analysis diagnostics
trawl --pkg ./cmd/server --entry Handle --stats --log-level off | jq .stats

# Check DFS coverage (nodes visited vs total graph size)
trawl --pkg ./cmd/server --entry Handle --stats --log-level off | jq '.stats | {coverage: (.nodes_visited / .call_graph_nodes * 100 | round | tostring + "%"), load_ms: .load_duration_ms}'
```
