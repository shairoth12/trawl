---
status: accepted
---

# RTA graph is built after entry-point resolution

RTA needs its roots up front, and the root is the entry function the user named. `analysis.Load` therefore returns a nil `Graph` for `--algo rta`; `cmd/trawl` resolves the entry and calls `rta.Analyze([]*ssa.Function{fn}, true)` itself. A nil graph after `Load` under RTA is by design.
