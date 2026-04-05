---
name: trawl-config
description: >
  Generate a trawl.yaml config file by reading a Go codebase to discover internal
  wrapper libraries that trawl cannot recognize by default. Use this skill before
  running trawl whenever a user says "set up trawl for this codebase", "trawl isn't
  detecting our internal libraries", "create a trawl config", "configure trawl
  indicators", "trawl misses calls through our wrappers", or when preparing to run
  trawl on a codebase that uses internal client libraries, SDK wrappers, or custom
  service abstractions. Run this skill first, then run the trawl skill.
compatibility: Requires Go codebase with go.mod. No trawl installation needed for this step.
metadata:
  author: shairoth12
  version: 1.0.0
---

> This skill produces a `trawl.yaml` config file. Once it exists, pass it to trawl
> with `--config trawl.yaml`. The config only needs to cover what trawl's built-in
> indicators miss — do not duplicate built-ins.

---

## When you need a config file

A config file is NOT always required. trawl's 13 built-in indicators already cover:

```
net/http · google.golang.org/grpc · github.com/go-redis/redis
github.com/redis/go-redis · cloud.google.com/go/pubsub
cloud.google.com/go/datastore · cloud.google.com/go/firestore
database/sql · github.com/lib/pq · github.com/jackc/pgx
github.com/elastic/go-elasticsearch · github.com/hashicorp/vault/api
go.etcd.io/etcd/client
```

A config file is needed when the codebase:
- Uses **internal wrapper libraries** around any of the above (e.g. `internal/cache` wrapping go-redis)
- Uses **external services not in the built-in list** (Kafka, MySQL, DynamoDB, NATS, S3, etc.)
- Uses a built-in library for a **different service type** (e.g. `database/sql` connecting to MySQL, not Postgres)

If none of these apply, skip this skill and run trawl directly.

---

## Step 1: Read go.mod

```bash
cat go.mod
```

Scan `require` blocks for known external service libraries. Flag any that appear:

| If you see... | Service category |
|---|---|
| `github.com/go-redis/redis`, `github.com/redis/go-redis` | Redis |
| `google.golang.org/grpc` | gRPC |
| `cloud.google.com/go/pubsub` | Pub/Sub |
| `cloud.google.com/go/datastore` | Datastore |
| `cloud.google.com/go/firestore` | Firestore |
| `github.com/lib/pq`, `github.com/jackc/pgx` | Postgres |
| `github.com/elastic/go-elasticsearch` | Elasticsearch |
| `github.com/hashicorp/vault/api` | Vault |
| `go.etcd.io/etcd/client` | Etcd |
| `github.com/segmentio/kafka-go`, `github.com/confluentinc/confluent-kafka-go` | Kafka |
| `github.com/aws/aws-sdk-go`, `github.com/aws/aws-sdk-go-v2` | AWS (S3, DynamoDB, SQS, etc.) |
| `go.mongodb.org/mongo-driver` | MongoDB |
| `github.com/go-sql-driver/mysql` | MySQL |
| `github.com/nats-io/nats.go` | NATS |

Also note any service-like internal dependencies (other modules owned by the same org).

---

## Step 2: Find internal packages that import external service libraries

For each flagged external library, search which internal packages import it:

```bash
# Find all .go files that import a specific external library
grep -rn "\"github.com/redis/go-redis" --include="*.go" .
grep -rn "\"google.golang.org/grpc" --include="*.go" .
grep -rn "\"github.com/segmentio/kafka-go" --include="*.go" .
# etc.
```

Collect the set of internal package paths that appear in these results.

---

## Step 3: Determine which are wrappers

Not every package that imports an external library is a wrapper. Distinguish:

**Is a wrapper — add to config:**
- Lives under `internal/`, `pkg/`, or a clearly shared location (not `cmd/`)
- Exports an interface or struct that other internal packages depend on
- Has a name suggesting abstraction: `cache`, `store`, `client`, `pubsub`, `queue`, `db`, `redis`, `kafka`, etc.
- Other packages import THIS package instead of the external library directly

**Is NOT a wrapper — skip:**
- `cmd/` packages using the external library directly (they're callers, not wrappers)
- `main.go` files wiring things together
- Test files (`*_test.go`)
- The package imports the library only for type assertions or config structs

To confirm a wrapper, read its source briefly:

```bash
# List exported symbols — a wrapper typically exports a client/store type
grep -n "^func\|^type\|^var" internal/cache/*.go
```

If the package's exported API abstracts the external library behind an interface or thin struct, it's a wrapper.

---

## Step 4: Determine the config entries

For each confirmed wrapper, decide:

**`package`**: The wrapper's Go import path (as it appears in other packages' imports).

**`service_type`**: The service label. Use trawl's built-in labels when applicable
(`HTTP`, `GRPC`, `REDIS`, `PUBSUB`, `DATASTORE`, `FIRESTORE`, `POSTGRES`,
`ELASTICSEARCH`, `VAULT`, `ETCD`). For unlisted services, use a clear uppercase label
(`KAFKA`, `MYSQL`, `MONGODB`, `S3`, `DYNAMODB`, `NATS`, etc.).

**`wrapper_for`**: List the external library import paths the wrapper imports. This
ensures calls through both the wrapper AND the underlying library are detected as
`resolved_via: direct, confidence: high`. If unsure, grep the wrapper's source for its
own imports:

```bash
grep "^import\|\"" internal/cache/cache.go | grep -v "^//"
```

**`skip_internal`**: Set `true` for most wrappers — prevents noise from the wrapper
package's own `/internal/` subpackages.

---

## Step 5: Handle service type overrides

If the codebase uses a built-in library for a different purpose than trawl assumes:

```bash
# Check what database/sql is actually connecting to
grep -rn "sql.Open\|dsn\|driver" --include="*.go" . | head -20
```

If `database/sql` connects to MySQL (not Postgres), add an override:

```yaml
indicators:
  - package: "database/sql"
    service_type: "MYSQL"
```

User indicators are checked before built-ins — the override takes precedence.

---

## Step 6: Write trawl.yaml

Assemble the config. Order matters: more specific prefixes must come before less specific ones.

```yaml
indicators:
  # Internal wrappers (specific paths first)
  - package: "github.com/myorg/cache"
    service_type: "REDIS"
    skip_internal: true
    wrapper_for:
      - "github.com/redis/go-redis/v9"

  - package: "github.com/myorg/messaging"
    service_type: "KAFKA"
    skip_internal: true
    wrapper_for:
      - "github.com/segmentio/kafka-go"

  - package: "github.com/myorg/httpclient"
    service_type: "HTTP"
    skip_internal: true
    wrapper_for:
      - "net/http"

  # Service type overrides (if needed)
  - package: "database/sql"
    service_type: "MYSQL"
```

Save as `trawl.yaml` at the root of the repository (or wherever trawl will be run from).

---

## Step 7: Verify

Run a quick sanity check before handing off to the trawl skill:

```bash
# Confirm trawl loads the config without errors
trawl --pkg . --entry main --config trawl.yaml 2>&1 | head -5
```

A config validation error looks like:
```
trawl: loading config: indicator 0: package must not be empty
```

Fix any reported issues, then proceed with the trawl skill.

---

## What NOT to add

- **Don't duplicate built-ins.** If `github.com/redis/go-redis` is already built-in, don't re-add it — only add the wrapper that uses it.
- **Don't add every internal package.** Only add packages that other code goes through to reach an external service. Direct callers (cmd/, main packages) are not wrappers.
- **Don't add test doubles.** Mock implementations (`MockStore`, `FakeClient`) are intentionally filtered by trawl — don't add them as indicators.
- **Don't over-specify `wrapper_for`.** List only the external libraries the wrapper directly imports, not transitive dependencies.
