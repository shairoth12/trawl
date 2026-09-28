---
status: accepted
---

# Method resolution through `types.Named.Methods()`

Methods are not present in `ssaPkg.Members` under a `Type.Method` key, so the obvious lookup finds nothing. Entry points of the form `Type.Method` are resolved by walking the type's declared method list and asking the program for the function:

```
ssaPkg.Members[typeName] → (*ssa.Type) → .Type() → (*types.Named) → .Methods() → prog.FuncValue(method)
```

This covers both pointer and value receivers with one path.
