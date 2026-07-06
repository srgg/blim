# DEFERRED WORK: coroutine-per-callback scheduler (safe blocking inside callbacks)

**Status:** Proposed / Deferred — future feature, NOT yet implemented.
**Design doc:** `RFC-001-lua-callback-coroutine-scheduler.md` in the repo root (full problem statement, design, effort, risks).
**Origin:** issue #3.

## Why this exists
The Lua runtime uses a single `lua_State` guarded by `LuaEngine.stateMutex`. Primitives that
release the mutex while blocking — `blim.sleep` (`internal/lua/api.go: registerSleepFunction`),
`io.read` (`internal/lua/lua_engine.go: registerIOReadContextAwareInternal`), and transitively
`blim.term.read_char(wait_ms)` (`examples/lib/blim.lua`) — are safe from the main script but
corrupt the shared state (SIGSEGV in `lua_getinfo`) when called from **inside a subscription/PTY
callback**, because another `DoWithState` reenters the same in-flight `lua_State` from another
goroutine.

## Two-stage plan
1. **Now (issue #3 fix — DONE):** `LuaEngine.raiseIfInCallback(L, op)` guard on `inCallbackCount > 0`
   raises a clean Lua error instead of crashing/blocking. Covers `blim.sleep` + `io.read` (the
   mutex-releasing SIGSEGV vector) AND the blocking BLE ops `characteristic.read()`/`.write()` and
   `blim.subscribe` (synchronous CCCD write) — they hold the state while blocking, not the crash
   vector but still unsafe. `cancel()` (self-cancel from a callback) stays allowed. The counter
   was moved from `LuaAPI` to `LuaEngine` (owner of the state/mutex, so `io.read` can see it) and its
   inc/dec made panic-safe (`defer`). Regression tests in `internal/lua/api_test.go` under
   `TestErrorHandling` ("<op> inside subscription callback raises guard error" for all four ops).
2. **Later (this RFC / Variant 1):** run each callback on its own `lua_newthread` (shared globals,
   private stack) driven by `lua_resume`; blocking primitives `lua_yield` to a Go cooperative
   scheduler (timer wheel + completion routing) instead of releasing the mutex. Preserves the
   shared-globals model, eliminates the corruption. Effort ~2-4 weeks, high risk (rewrites the
   most concurrency-sensitive code). golua exposes `NewThread/Resume/Yield`; build is LuaJIT
   (Lua 5.1 ABI) so C-function yields use the `return lua_yield` idiom with no C continuation.

## Rejected alternative
Independent `lua_State` per subscription (`NewState`): true parallelism but shares nothing;
callbacks are closures over main-script globals/upvalues and cannot cross VMs. Breaks the model.

## Trigger to revisit
When "callbacks must be able to block/sleep/read a key" becomes a real product requirement
(e.g. interactive TUI redraw waiting on a keypress from a notification callback — the original
field crash). Until then: do BLE ops / waits from the main loop; callbacks only set flags/cache.
