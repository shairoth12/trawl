---
status: accepted
---

# An empty module path disables the boundary

`LoadResult.Module` is empty for GOPATH workspaces without a `go.mod`. `strings.HasPrefix(pkgPath, "")` is then always true, so the walker recurses everywhere. This is the intended fallback, not a bug: with no module there is no meaningful boundary to enforce, and the detector still stops recursion at indicator packages.
