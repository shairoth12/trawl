# Output Format

trawl writes a single JSON object to stdout. This document is the authoritative schema reference.

## Top-Level Object

```json
{
  "entry_point":    "string",
  "package":        "string",
  "external_calls": [ ... ],
  "deduplicated":   true
}
```

```
Field          │ Type     │ Always present │ Description
───────────────┼──────────┼────────────────┼─────────────────────────────────────────────
entry_point    │ string   │ YES            │ Fully-qualified SSA name of resolved entry point
package        │ string   │ YES            │ Import path of the directly-analyzed package
external_calls │ array    │ YES            │ List of ExternalCall objects. Never null, may be empty.
deduplicated   │ boolean  │ NO             │ Present and true only when --dedup was used
```

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

### `resolved_via`

```
Value                      │ Meaning                                          │ Typical confidence
───────────────────────────┼──────────────────────────────────────────────────┼───────────────────
direct                     │ Import path matched an indicator prefix          │ high
                           │ (including wrapper_for expansions)               │
mock_inference             │ Mock type detected; service type inferred from   │ medium
                           │ the mock's package imports                       │
cross_module_inference     │ External module; service type inferred from      │ low
                           │ 2-level transitive imports                       │
```

### `confidence`

```
Value   │ Meaning
────────┼───────────────────────────────────────────────────────────────
high    │ Direct indicator match. Reliable.
medium  │ Mock inference. Likely correct, depends on import conventions.
low     │ Transitive import inference. Treat as a hint, verify manually.
```

### `service_type` (built-in values)

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

`short_function` and `short_call_chain` are computed by `trawl.ShortenName()`:

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
```
