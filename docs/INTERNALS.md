# Internals

Deep reference for each internal package. Read [ARCHITECTURE.md](ARCHITECTURE.md) first for the big picture.

---

## Package `internal/analysis`

**Files**: `analysis.go`, `deps.go`, `resolve.go`
**Purpose**: Load Go packages into SSA form, construct call graphs, resolve entry points.

### `Load(ctx, opts Options) (*LoadResult, error)`

Stages:

```
1. Validate opts.Deps ("" → auto; anything but auto/none → error)
2. loadPackages(): packages.Config with all NeedX modes, pattern + scope
   patterns in a single packages.Load call, package errors → ErrPackageLoad
   └─ Special case: toolchain version mismatch → descriptive error
3. ctx.Err() check (cancellation gate)
4. modulePathOf() — module of the primary package (falls back to first pkg with a Module)
5. Deps == auto: selectDependencyPkgs() (deps.go)
   ├─ modulePkgs: every loaded package of the analyzed module not already initial
   ├─ round 1: collect interfaces invoked by module packages (TypesInfo.Selections,
   │  receiver of the selected method, Origin() for generics; stdlib skipped)
   │  → candidate packages (non-stdlib, non-indicator) declaring a concrete
   │  non-"Mock*" named type that implements one (types.Implements on *T;
   │  method-name superset for generic types/interfaces)
   ├─ rounds 2..3: same, scanning only the packages picked in the previous round
   └─ cap: external list truncated to MaxDependencyPkgs (default 200), skipped count kept
6. createProgram(pkgs, withBodies): ssa.NewProgram + CreatePackage once per package
   (packages.Visit post-order); syntax/TypesInfo attached for initial and selected packages
7. buildProgram(): Package.Build per package in ≤GOMAXPROCS goroutines,
   panic → error mentioning --deps none
8. resolveSSAPkg() — find the primary SSA package
   └─ With scope: match by directory path (GoFiles[0] dir == abs(dir/pattern))
   └─ Without scope: ssaPkgs[0]
9. Branch on algo:
   ├─ RTA  → return (Graph: nil)
   ├─ CHA  → graph = cha.CallGraph(prog)
   └─ VTA  → initial = cha.CallGraph(prog)
              graph = vta.CallGraph(allFunctions, initial)
```

**Key API**: `createProgram` mirrors `ssautil.Packages(pkgs, mode)` (which attaches syntax only to initial packages). `ssautil.CreateProgram` takes deprecated `*loader.Program`, NOT `[]*packages.Package`. `Program.Build` is avoided because its per-package goroutines cannot recover panics.

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

type DepPolicy string  // "auto" | "none"

type Options struct {
    Dir, Pattern      string
    Algo              Algo
    Scope             []string
    Deps              DepPolicy         // "" → DepAuto
    MaxDependencyPkgs int               // 0 → DefaultMaxDependencyPkgs (200)
    IsIndicator       func(string) bool // indicator packages never receive bodies
}

type LoadResult struct {
    Prog                  *ssa.Program     // full SSA program
    Graph                 *callgraph.Graph // nil for RTA
    SSAPkg                *ssa.Package     // primary analyzed package
    Module                string           // module path from go.mod
    PackagesLoaded        int
    DependencyPkgs        []string         // same-module (sorted), then external by round then path
    DependencyPkgsSkipped int
    PackagesAnalyzed      int              // initial + len(DependencyPkgs)
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

**Files**: `walker.go`, `merge.go`, `export_test.go`
**Purpose**: DFS traversal of an SSA call graph. Detects external service calls reachable from an entry point.

### `New(graph, detector, opts Options) *Walker`

`Options{Module, DependencyPkgs, Fset, Log}`. `DependencyPkgs` is
`LoadResult.DependencyPkgs` — the packages whose bodies the walker may enter.
Not safe for concurrent use. A nil `Log` disables debug logging. Import
inference results are memoized per `*types.Package` for the walker's lifetime.

### `Walk(entry) ([]ExternalCall, WalkStats, error)`

```
Entry node not in graph?
├─ graph == nil → error: "did you forget rta.Analyze?"
└─ node == nil  → error: "try --algo vta"

Reset per-walk state (visited, counters)
hits = dfs(entryNode, [entry.String()], cx=nil)
Return mergeByPosition(hits) (always non-nil slice), WalkStats
```

`WalkStats`: `NodesVisited` (module nodes + dependency node entries, one per
crossing), `EdgesExamined`, `UnresolvedInvokes`, `TracedCrossings`.

### DFS Decision Tree

`dfs(node, chain, cx)` — `cx` is nil in module code and a `*crossing` inside
dependency bodies. The visited set is `w.visited` for module nodes and
`cx.visited` per crossing. Every emission goes through `record(pos, cx, …)`:
with `cx == nil` the hit is the edge's own site; otherwise it is rewritten to
the crossing's boundary site with `resolved_via: cross_module_trace`.

For each outgoing edge from current node:

```
fn = calleeFunc(edge); fn == nil? → skip
pkgPath, typesPkg = calleePkg(fn)
  Package() → Origin().Package() → Object().Pkg() → receiver's named type
pkgPath == ""? → skip (unknown_package)
isUbiquitousDispatch(edge)? → skip

isMockMethod(fn)?
├─ YES → mockHits:
│   cx != nil, or same module, or built dependency, or not invoke → skip
│   inferFromTypesPkg == ""? → skip
│   EMIT (interface label): mock_inference / medium,
│        upgraded to direct / high if det.Detect(pkgPath)
│
det.Detect(pkgPath)? → EMIT via record (direct / high); do not recurse
inModule(pkgPath)?  → RECURSE dfs(callee, next, nil)   ← attribution resets
deps[pkgPath]?      → enterDependency:
│   cx != nil → RECURSE dfs(callee, next, cx)
│   cx == nil → open crossing{pos, function, pkgPath}; tracedCrossings++
│               hits = dfs(callee, next, crossing)
│               no hit at the boundary pos AND invoke edge?
│               └─ inferFromTypesPkg → EMIT cross_module_inference / low
invoke edge?        → inferFromTypesPkg → EMIT via record (cross_module_inference / low)
otherwise           → skip (outside_module)
```

After the edge loop, `unresolvedInvokes(node)` yields every interface call
site in the node's body with no edge to a non-mock callee, and
`classifyUnresolved(site)` decides:

```
interface type not *types.Named, universe (error), type parameter, or ubiquitous → ignore
declared in stdlib → ignore
unresolved++
det.Detect(iface pkg)      → EMIT interface_dispatch / high
inModule(iface pkg)        → count only (same_module_no_implementor)
inferFromTypesPkg(iface pkg) != "" → EMIT interface_dispatch / low
```

Finally `mergeByPosition(hits)` (merge.go) collapses hits with equal
`(ServiceType, pos)`: higher confidence wins; on a tie the interface label
("Iface.Method") beats a concrete "(recv).Method" name; otherwise first wins.
Hits with `token.NoPos` are never merged.

### Filter Functions

**`isUbiquitousDispatch(edge)`**: Returns true if the edge is an interface dispatch (`cc.IsInvoke()`) on one of these types:
- `error` (builtin, `Pkg() == nil`)
- `fmt.Stringer`, `io.Reader`, `io.Writer`, `io.Closer`, `io.ReadCloser`, `io.WriteCloser`, `io.ReadWriteCloser`, `context.Context`, `sort.Interface`

CHA resolves these to every implementor in the program, producing noise.

**`isMockMethod(fn)`**: Returns true if fn has a receiver type whose name starts with `"Mock"`. Mockery-generated mocks satisfy interfaces structurally → CHA routes through them into testify internals.

**`interfaceMethodLabel(cc)`**: Returns `"InterfaceType.MethodName"` from an invoke call site. Used instead of concrete mock type names in output.

### Inference Functions

**`inferFromTypesPkg(typesPkg)`**: Checks if typesPkg imports (direct or 1-level transitive) a package that the detector recognizes. Returns the matched `ServiceType` or `""`. Memoized per package on the Walker.

It checks 2 levels of imports to handle wrapper patterns:
```
rediscache → infra/redis → go-redis  (2 levels)
```

### Helper Functions

**`calleePkg(fn)`**: Import path and `*types.Package` owning fn: `Package()`, else `Origin().Package()` (generic instantiations), else `Object().Pkg()` (synthetic wrappers), else the receiver's named type.

**`receiverTypesPkg(fn)`**: `fn.Signature.Recv()` → pointer deref → `*types.Named` → `Obj().Pkg()`; nil if any step fails.

**`calleeFunc(edge)` / `isInvoke(edge)` / `sitePos(edge)`**: nil-safe accessors for the callee function, invoke-ness and call-site position (`token.NoPos` for synthetic edges).

**`posAt(pos)`** / **`hitAt(pos, …)`**: Resolve a position to file/line and build a `hit` (an `ExternalCall` plus its position, the merge key).

**`appendCopy(chain, elem)`**: Always allocates a new slice. Prevents DFS branch corruption through Go's slice aliasing.

Removed in favour of the above: `isMockReceiver`, `receiverPkgPath`, `inferFromImports`, `posFile`, `posLine`.

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
