---
status: accepted
---

# Never call `graph.DeleteSyntheticNodes()`

The VTA pipeline produces a graph with synthetic nodes, and `DeleteSyntheticNodes()` looks like an obvious cleanup step. Calling it strips direct call edges — a ~6000-node graph collapses to ~180 nodes with no path from entry points to the standard library. The walker instead handles synthetic nodes implicitly through the module-boundary and detector checks, so the graph is used exactly as VTA returns it.
