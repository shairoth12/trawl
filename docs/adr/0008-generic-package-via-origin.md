---
status: accepted
---

# Generic instantiations recover their package via `Origin()`

Go SSA sets `Package()` to nil on every generic instantiation, so a callee such as `Map[string, string]` looks package-less and used to be dropped. `calleePkg(fn)` falls back to `fn.Origin()` (the generic declaration, which has a package), then `fn.Object()`, then the receiver's `*types.Named`. Edges to top-level generic helpers are now classified like any other.
