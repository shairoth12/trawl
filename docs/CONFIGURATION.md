# Configuration

## Overview

trawl supports an optional YAML configuration file passed via `--config`. The file declares custom service indicators that extend or override the 13 built-in ones.

## File Format

```yaml
indicators:
  - package: "import/path/prefix"
    service_type: "LABEL"
    skip_internal: false          # optional, default false
    wrapper_for:                  # optional
      - "wrapped/library/prefix"
      - "another/wrapped/prefix"
```

**Top-level key**: `indicators` (optional, list — omit or leave empty to use only built-in indicators)

## Indicator Fields

```
Field          │ Type          │ Required │ Default │ Description
───────────────┼───────────────┼──────────┼─────────┼────────────────────────────────────────
package        │ string        │ YES      │ —       │ Import path prefix to match
service_type   │ string        │ YES      │ —       │ Label assigned to matched calls
skip_internal  │ bool          │ no       │ false   │ Exclude /internal/ subpackages
wrapper_for    │ list<string>  │ no       │ []      │ Extra prefixes → same service type
```

## Matching Algorithm

```
Given importPath and indicator.Package:

1. strings.HasPrefix(importPath, indicator.Package)?
   └─ NO → try next indicator

2. Boundary check:
   rest = importPath[len(indicator.Package):]
   rest != "" AND rest[0] != '/'?
   └─ YES → skip (prevents "redis" matching "redis2")

3. SkipInternal check (only if skip_internal: true):
   rest contains "/internal/" or ends with "/internal"?
   └─ YES → skip

4. MATCH → return (service_type, true)
```

### Priority Rules

```
1. User indicators are checked BEFORE built-in indicators
2. First matching prefix wins
3. More specific prefixes should appear BEFORE less specific ones

Example ordering:
  - "github.com/org/googleapi"    ← must come before
  - "github.com/org/google"       ← because "google" prefix-matches "googleapi"
```

## WrapperFor Expansion

`wrapper_for` entries are expanded into separate indicators at `detector.New()` time:

```yaml
# This config entry:
- package: "github.com/org/rediscache"
  service_type: "REDIS"
  skip_internal: true
  wrapper_for:
    - "github.com/redis/go-redis/v9"

# Becomes these indicators (in order):
# 1. { package: "github.com/org/rediscache",     service_type: "REDIS", skip_internal: true }
# 2. { package: "github.com/redis/go-redis/v9",  service_type: "REDIS", skip_internal: true }
```

Both entries produce `resolved_via: "direct"` and `confidence: "high"` matches.

## Validation

`Config.Validate()` runs automatically during `LoadConfig()`. It rejects:

```
Error condition                           │ Message format
──────────────────────────────────────────┼───────────────────────────────────────────
Empty package field                       │ indicator N: package must not be empty
Empty service_type field                  │ indicator N (pkg): service_type must not be empty
Empty wrapper_for entry                   │ indicator N (pkg): wrapper_for[M] must not be empty
```

An empty `package: ""` is particularly dangerous — it causes `strings.HasPrefix` to match ALL import paths.

## Built-in Indicators

These are always active, appended after user indicators (lower priority):

```
Package prefix                           │ ServiceType     │ SkipInternal
─────────────────────────────────────────┼─────────────────┼─────────────
github.com/go-redis/redis                │ REDIS           │ true
github.com/redis/go-redis                │ REDIS           │ true
google.golang.org/grpc                   │ GRPC            │ true
net/http                                 │ HTTP            │ true
cloud.google.com/go/pubsub              │ PUBSUB          │ true
cloud.google.com/go/datastore           │ DATASTORE       │ true
cloud.google.com/go/firestore           │ FIRESTORE       │ true
database/sql                             │ POSTGRES        │ true
github.com/lib/pq                        │ POSTGRES        │ true
github.com/jackc/pgx                     │ POSTGRES        │ true
github.com/elastic/go-elasticsearch      │ ELASTICSEARCH   │ true
github.com/hashicorp/vault/api           │ VAULT           │ true
go.etcd.io/etcd/client                   │ ETCD            │ true
```

## Example: Override a Built-in

```yaml
indicators:
  # Override: database/sql is MySQL in this project, not Postgres
  - package: "database/sql"
    service_type: "MYSQL"
```

Because user indicators are checked first, this replaces the built-in POSTGRES classification for `database/sql`.

## Example: Wrapper Library

```yaml
indicators:
  # Internal cache wrapper that uses go-redis underneath
  - package: "github.com/org/cache"
    service_type: "REDIS"
    wrapper_for:
      - "github.com/redis/go-redis/v9"

  # Internal HTTP client
  - package: "github.com/org/httpclient"
    service_type: "HTTP"
    wrapper_for:
      - "net/http"
```

## Example: Custom Service Types

```yaml
indicators:
  - package: "github.com/org/bolt-client"
    service_type: "BOLT"

  - package: "github.com/org/kafka"
    service_type: "KAFKA"
    wrapper_for:
      - "github.com/segmentio/kafka-go"
```

Custom `service_type` strings can be any non-empty string. They appear as-is in the JSON output `service_type` field.

## CLI Usage

```bash
trawl --pkg ./cmd/server --entry HandleRequest --config trawl.yaml
```

If `--config` is not provided, only the 13 built-in indicators are active.
