---
name: trawl
description: >
  Analyze external service dependencies in a Go codebase using trawl static analysis.
  Use this skill whenever a user asks "what does X call?", "what services does this
  handler touch?", "what external calls need to be mocked for this function?", "show me
  the dependencies of this function", "what does HandleRequest reach?", "map the external
  calls from this entry point", or wants to identify calls to mock/stub in tests, audit
  Go service boundaries, understand microservice dependencies, or build a reachability
  map from any Go function. Invoke proactively when the user asks about Go service
  dependencies, external calls, mocking strategy, or what services a Go component
  depends on.
compatibility: Requires trawl CLI (go install github.com/shairoth12/trawl/cmd/trawl@latest) and Go 1.26+ (Go 1.21+ downloads it automatically during install). Target package must compile and have dependencies available.
metadata:
  author: shairoth12
  version: 1.2.0
---

> Note: trawl reads source code — it does NOT instrument at runtime. It cannot detect
> calls hidden behind `os.Exec` or string-based dynamic dispatch. The default VTA handles
> reflection-based DI (dig, fx) by falling back to CHA; use `--algo cha` only when
> `interface_dispatch` records remain.

---

## Examples

### Example 1: Identify calls to mock for a handler test

User says: *"What external calls does HandleOrder make? I need to set up mocks for its test."*

```bash
trawl --pkg ./cmd/server --entry HandleOrder
```

Result: Every HTTP, Redis, Postgres, etc. call reachable from `HandleOrder` — each
occurrence listed separately so each can be individually mocked.

### Example 2: Microservice with injected dependencies

User says: *"What needs to be mocked to test ProcessPayment? The payment service uses dig for DI."*

```bash
trawl --pkg ./internal/payment --entry ProcessPayment
```

Result: Full list of external calls including those resolved through reflect-based DI
wiring — every call site a test would need to stub. If the output still has
`interface_dispatch` records, rerun with `--algo cha`.

### Example 3: Audit with internal wrappers

User says: *"What does SubmitJob touch? We have internal Kafka and Redis wrappers."*

1. Create `trawl.yaml` declaring the wrappers (see Step 7)
2. Run: `trawl --pkg ./cmd/worker --entry SubmitJob --config trawl.yaml`

Result: Calls through internal wrappers classified correctly as KAFKA/REDIS with `confidence: high`,
each occurrence listed so the test setup can mock each call.

---

## Step 1: Find a good entry point

trawl needs a specific function to start from. Good candidates:

```bash
# HTTP handlers
grep -rn "func.*Handler\|func.*ServeHTTP\|func.*Handle(" --include="*.go" .

# gRPC server methods
grep -rn "func.*Server).*ctx context.Context" --include="*.go" .

# Worker / job entry points
grep -rn "func.*Process\|func.*Worker\|func.*Run(\|func.*Execute(" --include="*.go" .

# Temporal / queue activities
grep -rn "func.*Activity\|func.*Workflow" --include="*.go" .
```

Prefer specific entry points over broad ones. `HandleOrder` gives cleaner results than `main`.

Entry point formats trawl accepts:
- `HandleRequest` — top-level function by name
- `Handler.ServeHTTP` — method on a named type (pointer or value receiver)
- `ServeHTTP` — bare method name (errors if ambiguous across types)

---

## Step 2: Construct the command

```bash
trawl --pkg <pattern> --entry <name> [--algo vta|rta|cha] [--scope <patterns>] [--deps auto|none] [--dedup] [--stats] [--config trawl.yaml] [--log-level off|info|debug] [--log-file <path>] [--log-format text|json]
```

**`--stats`** appends a `stats` object to the JSON output with package count, call graph size, DFS traversal counters, and phase durations. Use it when a run is unexpectedly slow or you need to understand analysis scope. `load_duration_ms` covers package load + SSA + call graph construction for all algorithms (including RTA); `walk_duration_ms` is typically `0` (sub-millisecond). The `stats` key is absent when the flag is not passed.

**Logging flags** (trawl logs INFO-level stage progress to stderr by default):

| Flag | Default | Notes |
|------|---------|-------|
| `--log-level` | `info` | `off` silences all logs; `debug` adds per-edge decisions |
| `--log-file` | _(stderr)_ | Redirect logs to a file, leaving stderr clean for true errors |
| `--log-format` | `text` | `json` emits newline-delimited JSON — useful for agent frameworks that parse diagnostics |

**In agent contexts**, use `--log-file /tmp/trawl.log` so logs don't mix with errors on stderr.
The JSON result on stdout is always unaffected by logging flags.

**Do not use `--dedup` unless the task explicitly requires it.** trawl's primary use
case is identifying every external call that needs to be mocked or emulated in tests.
If the same Redis call is made 4 times, you need 4 mocks — deduplication would hide 3
of them. Use `--dedup` only when the goal is a unique inventory of services (e.g. "what
services does this component depend on?"), not when preparing a test setup.

**Use `short_function` and `short_call_chain`** from the output when presenting results
to users or passing to downstream tools. These fields strip module paths and generic type
parameters — they're designed for human and LLM readability. The full `function` and
`call_chain` fields are for programmatic identity checks.

---

## Step 3: Choose the algorithm

This is the most important decision. Getting it wrong means missed results — no error,
just silent gaps.

```
Does the package under --pkg directly instantiate its own concrete types?
│
├─ YES → --algo vta  (default, most precise)
│         VTA traces value flow through constructors and assignments.
│         Works when the wiring code is visible in the loaded packages.
│
├─ MAYBE, types are in a different package (constructor DI, wire) → --algo vta --scope ./cmd/server
│         Load the wiring package too, so VTA can see value flow.
│         Example: handler uses Store interface, NewServer(NewStore()) is in cmd/server.
│         wire generates plain constructor code (wire_gen.go), so it is the same case.
│
├─ Uses reflection-based DI (dig, fx) → --algo vta (default)
│         An interface call with no value flow gets the CHA callees instead.
│         If interface_dispatch records remain → --algo cha
│         (VTA keeps its own callees where some value does flow).
│         CHA trade-off: over-approximates (may include types never wired at runtime).
│
└─ Unsure, want broadest coverage → --algo cha --scope ./...
```

Quick signals in the codebase that indicate DI framework use:
- `dig.Provide`, `dig.Invoke`, `fx.Provide`, `fx.Options` → VTA; CHA if `interface_dispatch` remains
- `wire.Build`, `wire.NewSet` → VTA + scope of the package with `wire_gen.go`
- Constructor functions like `NewServer(deps ...)` passing interfaces → VTA + scope

---

## Step 4: Determine --scope

`--scope` loads extra packages into the SSA program without making them the analysis
target. It enriches the type universe that VTA and CHA can see.

| Situation | --scope value |
|-----------|---------------|
| Leaf package, wiring is in `./cmd/server` | `--scope ./cmd/server` |
| Multiple wiring packages | `--scope ./cmd/server,./cmd/worker` |
| DI framework, need all types | `--scope ./...` |
| Simple package, all types visible | (omit --scope) |

When in doubt for CHA, `--scope ./...` loads the whole module. It's slower but catches everything.

**Escalation path**: `--scope` is for *wiring* packages the target does not import.
Same-module packages the target imports and dependency modules implementing the
interfaces it calls are built automatically (`--deps auto`, the default) — do not
add dependency modules to `--scope`. `--deps none` disables that for a baseline.

---

## Step 5: Run

```bash
trawl --pkg ./cmd/server --entry HandleRequest --dedup --log-file trawl.log
```

INFO-level stage logs go to stderr by default. Use `--log-file` to redirect them to
a file so that stderr contains only true errors (warnings, fatal failures). Always
check stderr / the log file before trusting the result (see Troubleshooting below).

To run completely silently (stdout JSON only):
```bash
trawl --pkg ./cmd/server --entry HandleRequest --log-level off
```

---

## Step 6: Interpret the output

### Key fields per call

| Field | What to use it for |
|-------|-------------------|
| `short_function` | Display to user, pass to downstream tools |
| `short_call_chain` | Show the path from entry to call site |
| `service_type` | Classify and group results (HTTP, REDIS, etc.) |
| `confidence` | How much to trust this result |
| `resolved_via` | Why this confidence level was assigned |
| `file` + `line` | Link back to source (relative path from module root) |

### Confidence × resolved_via matrix

| resolved_via | confidence | What it means | How to treat it |
|---|---|---|---|
| `direct` | `high` | Import path matched a service indicator | Trust fully |
| `mock_inference` | `medium` | Mock type detected; service inferred from its imports | Likely correct; note it's inferred |
| `cross_module_inference` | `low` | External module; service type inferred from transitive imports | Treat as a hint; verify manually if it matters |
| `cross_module_trace` | `high` (sometimes `low`) | A dependency's function body was walked to a real backend call; `file`/`line` are the module-side call, `call_chain` continues into the dependency | Trust `high`; this is the line to mock |
| `interface_dispatch` | `high` or `low` | Interface call with no visible implementor; `high` when the interface's package is a configured indicator, `low` when inferred from its imports | `high`: trust; `low`: hint, the implementor may be bound by reflection |

For most agent workflows: surface `high` and `medium` confidently, flag `low` confidence
results as "inferred, not confirmed." A `low` `service_type` is the first detected import of
the interface's package and can name the wrong backend for a facade wrapping several
services — report it as "an external call" unless another record confirms the type.

### Zero results — diagnosis checklist

Empty `external_calls` is a valid result, but also the symptom of a missed call graph.
Before reporting "no external calls found," check:

1. Did you analyze the right package? Print `package` from the output — confirm it's what you expected.
2. Does the function actually call anything external? Read a few lines around the entry point to sanity check.
3. Is the algo wrong? If the package uses interfaces and DI, VTA may miss the dispatch. Try `--algo cha --scope ./...`.
4. Is scope missing? If the package only defines an interface and never instantiates it, VTA sees no concrete types. Add `--scope`.
5. Did the entry point resolve correctly? The `entry_point` field in output shows the fully-qualified SSA name — verify it looks right.
6. Run with `--stats` and check `stats.unresolved_invokes` — non-zero means an interface call had no implementor in the program; `stats.dependency_packages` shows what was auto-built. Compare against `--deps none` to see what the dependency bodies contributed.

---

## Step 7: Custom service indicators

First, check if a config file already exists:

```bash
ls trawl.yaml 2>/dev/null && echo "found" || echo "not found"
```

If found, pass it with `--config trawl.yaml` and skip the rest of this step.

If not found and the codebase uses internal wrapper libraries that trawl won't recognize
by default, use the `trawl-config` skill to generate one — it will read the codebase
and identify wrappers automatically. Then pass it here with `--config trawl.yaml`.

Otherwise, create `trawl.yaml` manually:

```yaml
indicators:
  # Internal Kafka client wrapper
  - package: "github.com/myorg/kafka"
    service_type: "KAFKA"
    wrapper_for:
      - "github.com/segmentio/kafka-go"   # underlying library also classified

  # Internal Redis cache wrapper
  - package: "github.com/myorg/cache"
    service_type: "REDIS"
    wrapper_for:
      - "github.com/redis/go-redis/v9"

  # Internal HTTP client
  - package: "github.com/myorg/httpclient"
    service_type: "HTTP"
    wrapper_for:
      - "net/http"
```

Then pass `--config trawl.yaml` to the command.

`wrapper_for` entries are expanded into separate indicators — calls through the wrapper
AND calls through the underlying library are both detected as `resolved_via: direct`
with `confidence: high`.

Two cases where **not** adding an entry is the right call:
- The wrapper wraps **several backends** (one type with a Postgres method and an HTTP
  method). An indicator labels every method the same; without it trawl walks into each
  method and reports the real backend per call.
- trawl can already **see the implementation** (same module, or a dependency package
  implementing an interface your code calls). It reports the backend as
  `cross_module_trace / high` without config. Add an entry only for a custom label or when
  the implementation is bound by reflection.

---

## Step 8: Escalation path

When results seem incomplete, try in this order:

1. Add `--scope ./cmd/server` (the package that wires types)
2. Widen scope: `--scope ./...`
3. Switch to `--algo cha --scope ./...` (broadest coverage, accepts false positives)
4. Add custom indicators in `trawl.yaml` for internal wrapper libraries
5. Try a different entry point that's higher in the call stack

---

## Troubleshooting

**`warning: trawl was built with goX.Y but host toolchain is goZ.W`**
The `go` on PATH is a newer Go release than the one trawl was built with, so loading may fail or the output may be
empty. (An older `go` on PATH is fine and gives no warning.)
Fix: rebuild trawl with that newer `go`: `go install github.com/shairoth12/trawl/cmd/trawl@latest`

**`resolving entry point "Foo": function not found`**
Entry point name doesn't match any function in the package.
Fix: Check spelling; try `Type.Method` format; grep for the function name: `grep -rn "func.*Foo" --include="*.go" .`

**`loading package "...": ...`**
Package doesn't compile or dependencies are missing.
Fix: Run `go build ./...` in the target directory first; resolve any compile errors.

**Empty results, no errors**
trawl ran cleanly but missed calls due to wrong algo or missing scope.
Fix: Work through the zero-results checklist in Step 6, then the escalation path in Step 8.

**`command not found: trawl`**
trawl is not installed or not on PATH.
Fix: `go install github.com/shairoth12/trawl/cmd/trawl@latest`

---

## Useful jq patterns

```bash
# All external calls (full list — default for test setup)
trawl ... | jq '.external_calls[] | {service_type, short_function, short_call_chain, file, line}'

# High-confidence calls only
trawl ... | jq '[.external_calls[] | select(.confidence == "high")]'

# Count call occurrences per service type (without --dedup, reflects actual mock count)
trawl ... | jq '[.external_calls[]] | group_by(.service_type) | map({type: .[0].service_type, count: length})'

# Unique service types reached (use --dedup for this — inventory mode, not mock setup)
trawl ... --dedup | jq '[.external_calls[].service_type] | unique'

# Diagnose slow analysis (package count + load time)
trawl ... --stats --log-level off | jq '.stats | {packages_loaded, load_duration_ms}'
```
