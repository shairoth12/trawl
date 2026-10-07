---
status: accepted
---

# The root package holds types only

The root package `github.com/shairoth12/trawl` exports the JSON output types (`Result`, `ExternalCall`, `AnalysisStats`), the config types (`Config`, `Indicator`) and the enum types with their constants. It has no functions and no analysis API. Helpers that used to be exported there (`IsMock`, `IsMockMethod`, `IsStandardLibrary`, `ShortenName`, `NewResult`, `LoadConfig`, `Config.Validate`) moved to `internal/detector`, `internal/walker` and `cmd/trawl`.

trawl is a CLI; its contract is the flags, the config file and the JSON output. After v1, every exported identifier can only be removed in a `/v2` module, so exporting helpers nobody needs would freeze them for good. An `Analyze` API was rejected for the same reason: it would freeze the analysis options and behavior. It can be added later without breaking anything; removing it could not be done that way.

`ResolvedVia` and `Confidence` are named string types, like `ServiceType`, because changing a field's type after v1 is a breaking change. `service_type` and `resolved_via` are open enums (new values in minor releases); `confidence` is closed. See docs/OUTPUT-FORMAT.md, "Compatibility".
