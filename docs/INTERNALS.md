# Internals

Deep reference for each internal package. Read [ARCHITECTURE.md](ARCHITECTURE.md) first for the big picture.

---

## Package `internal/analysis`

**Files**: `analysis.go`, `deps.go`, `resolve.go`
**Purpose**: Load Go packages into SSA form, construct call graphs, resolve entry points.

### `Load(ctx, opts Options) (*LoadResult, error)`

Stages:

```
1. Validate opts.DependencyPolicy ("" → auto; anything but auto/none → error)
2. loadPackages(): packages.Config with all NeedX modes, pattern + scope
   patterns in a single packages.Load call, package errors → ErrPackageLoad
   └─ Special case: toolchain version mismatch → descriptive error
3. ctx.Err() check (cancellation gate)
4. modulePathOf() — module of the primary package (falls back to first pkg with a Module)
5. DependencyPolicy == auto: selectDependencyPkgs() (deps.go)
   │
   ├─ modulePkgs:
   │    every loaded package of the analyzed module not already named by --pkg/--scope
   │
   ├─ round 1:
   │    list the interfaces that module code calls methods on
   │      (TypesInfo.Selections → receiver type of the selected method;
   │       Origin() for generics; standard-library interfaces skipped)
   │    pick every non-stdlib, non-indicator package that declares a concrete,
   │    named type, other than a mock, implementing one of them
   │      (types.Implements on *T; for generic types/interfaces a
   │       method-name superset check instead)
   │
   ├─ rounds 2..3:
   │    same again, but only looking at what the packages picked in the
   │    previous round call — this is how a facade's own interface
   │    dependencies resolve
   │
   └─ cap:
        dependency-module list cut at MaxDependencyPkgs (default 200);
        the number dropped is kept as DependencyPkgsSkipped

6. createProgram(pkgs, withBodies):
     ssa.NewProgram + one CreatePackage per package, dependencies before
     dependents (packages.Visit post-order)
     bodies (syntax + type info) attached for --pkg/--scope packages and
     the selected ones

7. buildProgram():
     Package.Build per package in ≤GOMAXPROCS goroutines
     panic → error mentioning --deps none

8. resolveSSAPkg() — find the primary SSA package
   ├─ With scope → match by directory path (GoFiles[0] dir == abs(dir/pattern))
   └─ Without scope → ssaPkgs[0]

9. Branch on algo:
   ├─ RTA → return (Graph: nil)
   ├─ CHA → graph = cha.CallGraph(prog)
   └─ VTA →
        initial = cha.CallGraph(prog)
        graph = vta.CallGraph(allFunctions, initial)
```

**Key API**: `createProgram` is a copy of `ssautil.Packages(pkgs, mode)` with one change: `ssautil` attaches bodies only to the `--pkg`/`--scope` packages, `createProgram` also attaches them to the selected dependency packages. `ssautil.CreateProgram` takes deprecated `*loader.Program`, NOT `[]*packages.Package`. `Program.Build` is avoided because its per-package goroutines cannot recover panics.

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
                    Skip mocks (mock.Mock / *gomock.Controller field)
                    Count matches:
                    ├─ 0 → error: not found
                    ├─ 1 → return it
                    └─ >1 → error: ambiguous
```

### Types

```go
type Algo string  // "vta" | "rta" | "cha"

type DependencyPolicy string  // "auto" | "none"

type Options struct {
    Dir, Pattern      string
    Algo              Algo
    Scope             []string
    DependencyPolicy  DependencyPolicy  // "" → DependencyAuto
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
Not safe for concurrent use. A nil `Log` disables debug logging. The result of
"which service does this package import" is cached per `*types.Package` for
the walker's lifetime.

### `Walk(entry) ([]ExternalCall, WalkStats, error)`

```
Entry node not in graph?
├─ graph == nil → error: "did you forget rta.Analyze?"
└─ node == nil  → error: "try --algo vta"

Reset per-walk state (visited, counters)
hits = dfs(entryNode, [entry.String()], cross=nil)
Return mergeByPosition(hits) (always non-nil slice), WalkStats
```

`WalkStats`: `NodesVisited` (functions in your module, plus dependency
functions counted once per entry from your code), `EdgesExamined`,
`UnresolvedInvokes`, `TracedCrossings` (how many times the walk went from your
code into a dependency; one interface call with two dependency implementations
counts twice).

### DFS Decision Tree

Terms: a **crossing** is the call in your code through which the walk entered
a dependency package; it remembers that call's position, callee and package.
`dfs(node, chain, cross)` runs with `cross == nil` while in your module and with a
`*crossing` while inside a dependency. Your module uses one shared visited set
(`w.visited`); each crossing has its own (`cross.visited`), so the same
dependency function can be re-walked from a different call in your code.

Every record goes through `record(pos, cross, …)`: with `cross == nil` it points at
the call itself; otherwise it points at the crossing's call in your code and
gets `resolved_via: cross_module_trace`.

For each outgoing edge from current node:

```
fn = calleeFunc(edge); fn == nil? → skip

pkgPath, typesPkg = calleePkg(fn)
  first non-nil: Package() → Origin().Package() → Object().Pkg() → receiver's named type
pkgPath == ""? → skip (unknown_package)

isUbiquitousDispatch(edge)? → skip

isMockMethod(fn)? → mockHits:
  inside a dependency, or same module, or the mock's package has bodies,
  or not an interface call → skip (the real implementation is reachable)
  inferFromTypesPkg == "" → skip
  otherwise →
    EMIT (interface label): mock_inference / medium
    upgraded to direct / high if det.Detect(pkgPath)

det.Detect(pkgPath)? → EMIT via record (direct / high); do not recurse

inModule(pkgPath)? → RECURSE dfs(callee, next, nil)
  (back in your code: findings are reported at the real line again)

deps[pkgPath]? → enterDependency:
  cross != nil → RECURSE dfs(callee, next, cross)
  cross == nil →
    open crossing{pos, function, pkgPath}; tracedCrossings++
    hits = dfs(callee, next, crossing)
    found nothing inside AND this was an interface call →
      guess from the package's imports → EMIT cross_module_inference / low

interface call? → guess from the package's imports → EMIT via record (cross_module_inference / low)

otherwise → skip (outside_module)
```

After the edge loop, `unresolvedInvokes(node)` lists every interface call in
this function that the call graph resolved to nothing, or only to mocks, and
`unresolvedHit(site, chain, cross)` decides what to do with each:

```
interface is not a named type, is `error`, a type parameter, or a very common interface → ignore
declared in stdlib → ignore
unresolved++
det.Detect(iface pkg)      → EMIT interface_dispatch / high
inModule(iface pkg)        → count only (same_module_no_implementation)
inferFromTypesPkg(iface pkg) != "" → EMIT interface_dispatch / low
```

Finally `mergeByPosition(hits)` (merge.go) collapses hits with equal
`(ServiceType, pos)` into one record: higher confidence wins; on a tie the
interface name ("Store.Get") beats a concrete method name ("(*sqlStore).Get");
otherwise the first wins.
Hits with `token.NoPos` are never merged.

### Filter Functions

**`isUbiquitousDispatch(edge)`**: Returns true if the edge is an interface dispatch (`cc.IsInvoke()`) on one of these types:
- `error` (builtin, `Pkg() == nil`)
- `fmt.Stringer`, `io.Reader`, `io.Writer`, `io.Closer`, `io.ReadCloser`, `io.WriteCloser`, `io.ReadWriteCloser`, `context.Context`, `sort.Interface`

CHA resolves these to every implementor in the program, producing noise.

**`isMockMethod(fn)`**: Returns true if fn's receiver is a mock according to `trawl.IsMock`: a struct with a field of type `mock.Mock` (testify, mockery) or `*gomock.Controller` (mockgen). It checks package and type names, not the type's own name, so a real `MockingbirdClient` is walked and a hand-written mock without such a field is walked too. Mocks satisfy interfaces, so the call graph sends interface calls through them, but their bodies only record the call for the test.

**`interfaceMethodLabel(cc)`**: Returns `"InterfaceType.MethodName"` from an invoke call site. Used instead of concrete mock type names in output.

### Inference Functions

**`inferFromTypesPkg(typesPkg)`**: Checks if typesPkg imports (directly, or via one of its direct imports) a package that the detector recognizes. Returns the matched `ServiceType` or `""`. Cached per package on the Walker.

It checks 2 levels of imports to handle wrapper patterns:
```
rediscache → infra/redis → go-redis  (2 levels)
```

### Helper Functions

**`calleePkg(fn)`**: Which package owns fn. First non-nil wins: (1) `fn.Package()`, (2) `fn.Origin().Package()` for generic instantiations, (3) `fn.Object().Pkg()` for synthetic wrappers, (4) the package of the receiver's named type.

**`receiverTypesPkg(fn)`**: `fn.Signature.Recv()` → pointer deref → `*types.Named` → `Obj().Pkg()`; nil if any step fails.

**`calleeFunc(edge)` / `isInvoke(edge)` / `sitePos(edge)`**: nil-safe accessors for the callee function, whether the edge is an interface call, and the call-site position (`token.NoPos` for synthetic edges).

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
