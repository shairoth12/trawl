# Internals

Deep reference for each internal package. Read [ARCHITECTURE.md](ARCHITECTURE.md) first for the big picture.

---

## Package `internal/analysis`

**Files**: `analysis.go`, `resolve.go`
**Purpose**: Load Go packages into SSA form, construct call graphs, resolve entry points.

### `Load(ctx, dir, pattern, algo, scopePatterns...) (*LoadResult, error)`

Stages:

```
1. Build packages.Config with all NeedX modes
2. Merge pattern + scopePatterns into single packages.Load call
3. Check for package errors
   └─ Special case: toolchain version mismatch → descriptive error
4. ctx.Err() check (cancellation gate)
5. ssautil.Packages(pkgs, ssa.InstantiateGenerics) → prog, ssaPkgs
6. prog.Build()
7. resolveSSAPkg() — find the primary SSA package
   └─ With scope: match by directory path (GoFiles[0] dir == abs(dir/pattern))
   └─ Without scope: ssaPkgs[0]
8. Extract module path from first pkg with non-nil Module
9. Branch on algo:
   ├─ RTA  → return (Graph: nil)
   ├─ CHA  → graph = cha.CallGraph(prog)
   └─ VTA  → initial = cha.CallGraph(prog)
              graph = vta.CallGraph(allFunctions, initial)
```

**Key API**: `ssautil.Packages(pkgs, mode)` is the correct modern API. `ssautil.CreateProgram` takes deprecated `*loader.Program`, NOT `[]*packages.Package`.

**Flag**: `ssa.InstantiateGenerics` — required so that generic type instantiations produce concrete SSA functions.

### `Resolve(result, entry) (*ssa.Function, error)`

```
Input contains "."?
├─ YES → resolveMethod(ssaPkg, entry)
│        Split on "." → typeName, methodName
│        ssaPkg.Members[typeName] → (*ssa.Type) → (*types.Named)
│        Iterate named.Methods() → prog.FuncValue(method)
│
└─ NO  → resolveFunc(ssaPkg, name)
          ssaPkg.Func(name) found?
          ├─ YES → return it
          └─ NO  → resolveBareMethod(ssaPkg, name)
                    Scan all Members for *ssa.Type
                    Skip types starting with "Mock"
                    Count matches:
                    ├─ 0 → error: not found
                    ├─ 1 → return it
                    └─ >1 → error: ambiguous
```

### Types

```go
type Algo string  // "vta" | "rta" | "cha"

type LoadResult struct {
    Prog   *ssa.Program       // full SSA program
    Graph  *callgraph.Graph   // nil for RTA
    SSAPkg *ssa.Package       // primary analyzed package
    Module string             // module path from go.mod
}

var ErrPackageLoad = errors.New("package load errors")
```

---

## Package `internal/detector`

**Files**: `detector.go`, `builtin.go`
**Purpose**: Classify Go import paths as known service types via prefix matching.

### `New(userIndicators) Detector`

```
1. Merge user indicators + builtin indicators (user first = higher priority)
2. Expand WrapperFor: each wrapper_for entry → synthetic Indicator
   with same ServiceType and SkipInternal
3. Return immutable detector struct
```

### `Detect(importPath) (ServiceType, bool)`

```
For each indicator in order:
  1. strings.HasPrefix(importPath, ind.Package)?
     └─ NO → next
  2. Boundary check: rest = importPath[len(ind.Package):]
     rest != "" && rest[0] != '/' → skip (prevents "redis" matching "redis2")
  3. SkipInternal check:
     rest contains "/internal/" or ends with "/internal" → skip
  4. MATCH → return (ind.ServiceType, true)

No match → ("", false)
```

### Built-in Indicators

```
Package prefix                           │ ServiceType
─────────────────────────────────────────┼────────────────
github.com/go-redis/redis                │ REDIS
github.com/redis/go-redis                │ REDIS
google.golang.org/grpc                   │ GRPC
net/http                                 │ HTTP
cloud.google.com/go/pubsub               │ PUBSUB
cloud.google.com/go/datastore            │ DATASTORE
cloud.google.com/go/firestore            │ FIRESTORE
database/sql                             │ POSTGRES
github.com/lib/pq                        │ POSTGRES
github.com/jackc/pgx                     │ POSTGRES
github.com/elastic/go-elasticsearch      │ ELASTICSEARCH
github.com/hashicorp/vault/api           │ VAULT
go.etcd.io/etcd/client                   │ ETCD
```

All builtins have `SkipInternal: true`.

---

## Package `internal/walker`

**Files**: `walker.go`, `export_test.go`
**Purpose**: DFS traversal of an SSA call graph. Detects external service calls reachable from an entry point.

### `New(graph, detector, module, fset, log) *Walker`

Creates a walker. Not safe for concurrent use. Pass `nil` for `log` to disable
debug logging (used in tests); otherwise pass the `*slog.Logger` from the CLI.

### `Walk(entry) ([]ExternalCall, error)`

```
Entry node not in graph?
├─ graph == nil → error: "did you forget rta.Analyze?"
└─ node == nil  → error: "try --algo vta"

Initialize visited map
Call dfs(entryNode, [entry.String()], visited)
Return results (always non-nil slice)
```

### DFS Decision Tree

For each outgoing edge from current node:

```
callee == nil or callee.Func == nil?
├─ YES → skip
└─ NO  → fn = callee.Func
          pkg = fn.Package()

pkg == nil? (Generic Instantiation Path)
├─ YES
│   recvPath = receiverPkgPath(fn)
│   recvPath == ""? → skip
│   isUbiquitousDispatch(edge)? → skip
│   Same module? → RECURSE DFS
│   External + invoke edge?
│   ├─ YES → inferFromTypesPkg → classify
│   │        Default: cross_module_inference / low
│   │        isMockReceiver? → mock_inference / medium
│   │        det.Detect(recvPath)? → direct / high
│   │        EMIT ExternalCall (with interface label)
│   └─ NO  → skip
│
└─ NO
    pkgPath = pkg.Pkg.Path()
    isUbiquitousDispatch(edge)? → skip
    isMockMethod(fn)?
    ├─ YES
    │   Same module? → skip entirely
    │   External + invoke?
    │   ├─ YES → inferFromImports → classify
    │   │        Default: mock_inference / medium
    │   │        det.Detect()? → direct / high
    │   │        EMIT ExternalCall (with interface label)
    │   └─ NO  → skip
    │
    ├─ NO
    │   det.Detect(pkgPath) matches?
    │   ├─ YES → EMIT ExternalCall (direct / high)
    │   │        Do NOT recurse into library internals
    │   │
    │   └─ NO
    │       Outside module boundary?
    │       ├─ YES + invoke edge?
    │       │   └─ inferFromImports → EMIT if match (cross_module / low)
    │       ├─ YES (no invoke) → stop, don't recurse
    │       └─ NO (inside module) → RECURSE DFS
```

### Filter Functions

**`isUbiquitousDispatch(edge)`**: Returns true if the edge is an interface dispatch (`cc.IsInvoke()`) on one of these types:
- `error` (builtin, `Pkg() == nil`)
- `fmt.Stringer`, `io.Reader`, `io.Writer`, `io.Closer`, `context.Context`, `sort.Interface`

CHA resolves these to every implementor in the program, producing noise.

**`isMockMethod(fn)`**: Returns true if fn has a receiver type whose name starts with `"Mock"`. Mockery-generated mocks satisfy interfaces structurally → CHA routes through them into testify internals.

**`isMockReceiver(fn)`**: Like `isMockMethod` but works when `fn.Package() == nil` (generic instantiations).

**`interfaceMethodLabel(cc)`**: Returns `"InterfaceType.MethodName"` from an invoke call site. Used instead of concrete mock type names in output.

### Inference Functions

**`inferFromImports(ssaPkg)`**: Checks if ssaPkg imports (direct or 1-level transitive) a package that the detector recognizes. Returns the matched `ServiceType` or `""`.

**`inferFromTypesPkg(typesPkg)`**: Same logic on `*types.Package` instead of `*ssa.Package`. Used for generic instantiations where the SSA package is nil.

Both check 2 levels of imports to handle wrapper patterns:
```
rediscache → infra/redis → go-redis  (2 levels)
```

### Helper Functions

**`receiverPkgPath(fn)`**: Extracts `fn.Signature.Recv()` → pointer deref → `*types.Named` → `Obj().Pkg().Path()`. Returns `""` if any step fails.

**`receiverTypesPkg(fn)`**: Same chain but returns `*types.Package` instead of path string.

**`appendCopy(chain, elem)`**: Always allocates a new slice. Prevents DFS branch corruption through Go's slice aliasing.

**`posFile(edge)`** / **`posLine(edge)`**: Extract source position from edge.Site. Guard against nil Site (synthetic edges) and invalid positions.

---

## Package `trawl` (root)

**Files**: `trawl.go`, `config.go`
**Purpose**: Type definitions and configuration loading.

### `ShortenName(s string) string`

```
Input: "(*github.com/foo/bar.Client).Do"

Step 1: stripGenericParams — remove [...] blocks
        "Cache[K, V].Set" → "Cache.Set"
        Iterative, handles nested brackets

Step 2: Find last "/" in string
        "(*github.com/foo/bar.Client).Do"
                               ^--- lastSlash

Step 3: Find first "." after lastSlash
        "(*github.com/foo/bar.Client).Do"
                               ^--- dotAfterSlash

Step 4: Preserve prefix before path start ("(*")
Step 5: Return prefix + everything after the dot

Output: "(*Client).Do"
```

### `LoadConfig(ctx, path) (Config, error)`

```
path == ""?  → return zero Config (no error)
os.ReadFile → yaml.Unmarshal → Config.Validate()
```

### `Config.Validate() error`

Checks every Indicator:
- `Package` must not be empty
- `ServiceType` must not be empty
- Each `WrapperFor` entry must not be empty

---

## Package `cmd/trawl`

**File**: `main.go`
**Purpose**: CLI orchestration. See [ARCHITECTURE.md](ARCHITECTURE.md) § Pipeline.

### `buildLogger(level, format, dst) (*slog.Logger, func(), error)`

Constructs the logger for the pipeline. Level `"off"` routes to `io.Discard`.
Otherwise opens `dst` as a file (or uses `os.Stderr` when empty) and returns a
`TextHandler` or `JSONHandler` keyed to `format`. The returned cleanup func
closes the file; callers must defer it.

Validates `format` before opening the file to avoid leaving an empty log file
on a bad `--log-format` value.

### `deduplicateCalls(calls) []ExternalCall`

```
Key: (ServiceType, ImportPath, Function)
For each call:
  key exists in seen?
  ├─ YES → shorter CallChain? → replace
  └─ NO  → append to result, record index

Result preserves insertion order of first occurrence.
```

### `toolchainWarning(hostGoVersion) string`

Compares `runtime.Version()` (compile-time) with `go env GOVERSION` (host). Non-empty warning string on mismatch. trawl uses `go/packages` which shells out to the host `go` command — a mismatch causes cryptic load errors.
