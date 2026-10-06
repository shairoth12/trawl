---
status: accepted
---

# VTA pipeline: CHA seed, then VTA refinement

`vta.CallGraph` needs an initial over-approximate graph to prune. We seed it with `cha.CallGraph(prog)` rather than a cheaper or hand-built graph: CHA alone over-approximates interface dispatch, VTA refines it by tracking value flow, and the pair is the combination the x/tools authors designed the API around.

```
CHA seed graph = cha.CallGraph(prog)
VTA graph      = vta.CallGraph(allFunctions, CHA seed)
```
