---
status: accepted
---

# `Program.Build` is not used

`ssa.Program.Build` starts a goroutine per package. A panic inside one of them cannot be caught by the code that called `Program.Build`, so a bad dependency package would crash the whole tool. Instead, `buildProgram` calls `Package.Build` itself for each package, at most `GOMAXPROCS` at a time, and turns a panic into an error that tells the user to retry with `--deps none`. Nothing is lost by doing this: in x/tools v0.50.0 `Program.Build` is exactly that loop plus a `WaitGroup`, and `Package.Build` may safely be called concurrently and more than once.
