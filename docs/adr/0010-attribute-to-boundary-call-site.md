---
status: accepted
---

# Findings inside a dependency are reported at the call in your code that entered it

The main question trawl answers is "what do I mock in this handler's test". That needs a line in the user's own code, not a line three calls deep inside a library. So when the walker follows a call from the user's module into a dependency package, it remembers that call (a `crossing` in the code: position, callee, package). Every backend hit found inside the dependency is reported with that call's `file`/`line`/`function` and `resolved_via: cross_module_trace`. The `call_chain` still shows the full path into the dependency, so the reader can see what was actually hit.
