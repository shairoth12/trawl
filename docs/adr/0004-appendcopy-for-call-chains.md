---
status: accepted
---

# `appendCopy` for DFS call chains

Go's `append` reuses the backing array when capacity allows, so sibling DFS branches sharing a prefix slice would overwrite each other's chains. `appendCopy` always allocates a fresh slice. The extra allocation per edge is negligible next to SSA construction and removes a whole class of silent output corruption.
