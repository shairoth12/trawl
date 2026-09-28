---
status: accepted
---

# Detector runs before the module-boundary check

The walker stops recursing at the module boundary. If the indicator check ran after that boundary check, every call into a third-party client library would be skipped as "outside the module" and never reported. The detector therefore runs first on every edge; a match is recorded and recursion stops there, whether or not the package is inside the module.
