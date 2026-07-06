# RFC-001 — Coroutine-per-callback scheduler for safe blocking inside subscription callbacks

- **Status:** Proposed / Deferred (tracked as future work; not part of the issue #3 crash fix)
- **Author:** engineering
- **Created:** 2026-07-06
- **Related:** issue #3 (SIGSEGV when a synchronous BLE read / `blim.sleep` / `blim.term.read_char` runs inside a subscription callback)
- **Supersedes/relates to:** the minimal `inCallbackCount` guard that turns the crash into a deterministic Lua error

---

## 1. Problem statement

The Lua runtime executes on a **single `lua_State`** guarded by one mutex (`LuaEngine.stateMutex`, a FIFO `FairLock`). All Lua work — the main script, BLE subscription callbacks, and PTY-data callbacks — runs on that one state, serialized by the mutex.

Certain Lua-exposed primitives **release the mutex while they block**, so that other work can run during the wait:

- `blim.sleep(ms)` — `internal/lua/api.go` (`registerSleepFunction`)
- `io.read(...)` — `internal/lua/lua_engine.go` (`registerIOReadContextAwareInternal`, *"Release mutex before blocking, like blim.sleep"*)
- reachable transitively via `blim.term.read_char(wait_ms)` (`examples/lib/blim.lua`), whose 10 ms polling loop calls `blim.sleep` on every step

This design is safe **from the main script** (depth-1 reentrancy: the main script sleeps, a callback runs on top of the parked state and unwinds cleanly — see `TestSleepReleasesLuaStateMutex`).

It is **unsafe from inside a callback**. A subscription callback already holds the state via `DoWithState -> stateMutex.Lock()`. When it calls one of the mutex-releasing primitives, the mutex is handed to **another** `DoWithState` (the main loop or a second subscription) which then executes Lua on the **same in-flight `lua_State`** from another goroutine. LuaJIT is not designed for reentrant/concurrent access to one VM; the interpreter's `CallInfo`/cframe chain is corrupted, and the process dies with `SIGSEGV` in `lua_getinfo` while the callback's `L.Call` error path tries to build a traceback (`addr=0x184` — a broken CallInfo, per the issue #3 stack trace).

The real-world trigger was a UI-redraw routine that waited for a keypress via `blim.term.read_char(wait_ms)` from a state-change notification callback.

### Immediate mitigation (separate, already scoped)

Issue #3 is closed by a **guard**: when `inCallbackCount > 0`, the primitives that are unsafe inside a callback raise a clean Lua error (`"<op> is not allowed inside a callback; defer to the main loop"`) instead of proceeding. This converts a `SIGSEGV` (and, for the blocking device ops, an engine freeze / potential deadlock) into a deterministic, recoverable failure. It does **not** let callbacks block/sleep.

- **Covered primitives:** the mutex-releasing ones — `blim.sleep` (`registerSleepFunction`), `io.read` (`registerIOReadContextAwareInternal`) — plus the blocking BLE ops `characteristic.read()` / `characteristic.write()` (`registerCharacteristicFunction`) and `blim.subscribe` (its `executeSubscription` does a synchronous CCCD write via `client.Subscribe`). The first two are the actual SIGSEGV vector (they release `stateMutex` mid-`L.Call`); the BLE ops do not release the mutex but block the callback goroutine while holding the state, so they are guarded for the same "don't do this in a callback" reason. `cancel()` (self-cancel from a callback) stays allowed — it does not go through a blocking path and is an intentional feature.
- **Chokepoint / ownership:** the guard is a single `LuaEngine.raiseIfInCallback(L, op)`. `inCallbackCount` lives on `LuaEngine` (owner of the state and mutex) so `LuaEngine`-registered `io.read` can consult it without depending upward on `LuaAPI`; its increment/decrement is panic-safe (`defer`).
- **Regression tests:** `internal/lua/api_test.go` → `TestErrorHandling` → subtests **"Lua: `<op>` inside subscription callback raises guard error"** for `blim.sleep`, `io.read`, `characteristic.read()`, `characteristic.write()`. Each subscribes, fires a notification whose callback calls the op, and asserts the clean guard error reaches stderr while the engine keeps running.

### Current workaround (until this RFC lands)

Perform BLE operations and any blocking wait (`sleep`, `read_char`, `io.read`) **only from the main loop**. Subscription callbacks must be non-blocking: only set flags / cache values. The main loop's `blim.sleep` releases the state so queued callbacks run. A UI redraw that needs a keypress or a characteristic read must run in the main loop, not in a notification callback.

### What this RFC addresses

This RFC proposes the **larger feature** the issue explicitly defers: *actually allowing* a subscription callback to perform a blocking wait (`sleep`, `read_char`, and ideally synchronous `char.read/write`) **without corrupting the shared state** — by restructuring callback execution around cooperatively-scheduled Lua coroutines.

---

## 2. Goals / Non-goals

### Goals

- A subscription (or PTY) callback may call `blim.sleep`, `io.read`, and `blim.term.read_char(wait_ms)` and **suspend cleanly** while other callbacks and the main loop continue to make progress.
- No corruption of the shared `lua_State`; no `SIGSEGV`.
- Preserve the existing **shared-globals programming model**: callbacks keep reading/writing the main script's globals and closures (`received_count = received_count + 1`, caches, flags).
- Deterministic cancellation (`scriptExecutionCtx`) and clean shutdown (`Close()` / shutdown hooks) still work.

### Non-goals

- **Parallel** Lua execution. LuaJIT shares one `global_State`; the mutex still serializes execution. The goal is clean *suspension*, not concurrency.
- Independent per-subscription VMs (see Rejected options).
- Changing the on-device BLE stack or the go-ble subscription fan-out.

---

## 3. Background: relevant golua / LuaJIT facts

- Build uses `-tags luajit` (`Makefile`) ⇒ **Lua 5.1 ABI** semantics.
- `aarzilli/golua` exposes the needed C-API: `State.NewThread()` (`lua_newthread`), `State.Resume()` (`lua_resume`), `State.Yield()` (`lua_yield`), `PushThread`, `ToThread`, `XMove`, plus `NewState` for fully independent states.
- `lua_newthread` **shares all global objects** (globals, tables, functions, GC heap) with the parent but has an **independent execution stack**. This is exactly what preserves the shared-globals model while giving each callback its own stack.

### LuaJIT 5.1 constraints (why this is more work than on Lua 5.4)

- A C function (our Go-registered functions are C functions) may yield only via the `return lua_yield(L, n)` idiom and has **no continuation**: after `resume`, control returns to the Lua caller, not back into the C function. Fine for `sleep`; for `char.read` the scheduler must push the result onto the thread's stack **before** resuming.
- The whole path from `Resume` down to the `yield` must be resumable. LuaJIT has a resumable VM (yield across `pcall`/metamethods/iterators), but each yield point must be validated by hand.
- Callbacks are currently invoked from Go via `L.Call` (`lua_pcall` from a C frame), which is **not** yield-friendly. Callbacks must instead be driven via `lua_resume` on a dedicated thread.

---

## 4. Proposed design (Variant 1: coroutine-per-callback + Go scheduler)

### 4.1 Core idea

Each callback invocation runs on its **own `lua_newthread`** (shared globals, private stack), driven by `lua_resume`. Blocking primitives no longer release the mutex; they **`lua_yield`** back to a Go-side cooperative scheduler, which parks the thread and resumes it when its wait completes (timer fired, stdin byte available, BLE read returned). Only one thread runs at a time (mutex/`global_State` invariant preserved), but a parked thread holds **no** live frames on any other thread's stack — eliminating the corruption vector.

### 4.2 Components

1. **Thread-driven callback dispatch.** Replace `L.Call(2, 0)` in `callLuaCallback` (`internal/lua/api.go`) — and the PTY path (`callPTYDataCallback`) — with: allocate/borrow a thread, push callback + args onto it, `Resume`. Interpret the result as one of `{completed, yielded, error}`. Keep the existing outer panic/recover and error-to-stderr behavior.

2. **Yieldable blocking primitives.** Rework as yield points that register a wakeup with the scheduler and `return lua_yield(...)`:
   - `blim.sleep` (`api.go`, `registerSleepFunction`) → register timer, yield.
   - `io.read` (`lua_engine.go`, `registerIOReadContextAwareInternal`) → register stdin-readable wakeup, yield; on resume, push the read bytes.
   - `characteristic().read()/write()` (`api.go`, `registerCharacteristicFunction`) → optional but desirable: start the BLE op asynchronously, yield, resume with the result. Without this, sync `char.read/write` still block the running thread (acceptable v1; can stay guarded).

3. **Go scheduler / runtime.** New component owning:
   - a **run queue** of resumable threads;
   - a **timer wheel** for `sleep`;
   - **completion routing** (`this BLE read finished → resume that thread`, `stdin readable → resume that thread`);
   - integration with `stateMutex`/`DoWithState` so a resume runs under the lock;
   - cancellation via `scriptExecutionCtx` (wake parked threads with an error on cancel);
   - shutdown: drain/abort parked threads in `Close()` and ordered shutdown hooks;
   - `inCallbackCount` (or its successor) maintained across yield/resume boundaries.

4. **GC anchoring.** Each `lua_newthread` must be anchored via a `LUA_REGISTRYINDEX` ref for its whole lifetime, or the GC may collect a parked thread → crash. Release the ref on completion. Reuse a small thread pool to bound allocation.

### 4.3 Ownership of state / where the guard logic moves

The callback-depth counter (`inCallbackCount`) currently lives on `LuaAPI`, but the mutex it protects lives on `LuaEngine`, and `io.read` (registered by `LuaEngine`) cannot see it today. The scheduler and the depth/reentrancy accounting should live on (or be reachable from) `LuaEngine`, behind a single chokepoint used by every blocking primitive.

---

## 5. Rejected / alternative options

- **Guard only (`inCallbackCount` → clean error).** Ships now, fixes the crash, but does **not** allow blocking in callbacks. This is the issue #3 fix, not this RFC.
- **Deferral API.** Callbacks only enqueue work; the main loop drains the queue and performs BLE ops / sleeps there. Small effort (days), formalizes the documented workaround, but changes the ergonomics (no straight-line blocking in callbacks). Reasonable interim if the coroutine work is not funded.
- **Independent `lua_State` per subscription (`NewState`).** Gives true parallelism (no shared mutex) but shares **nothing** — callbacks are closures over the main script's globals/upvalues, which cannot move across VMs. Would force script duplication + cross-VM message passing (actor model), breaking the current programming model. **Rejected.**

---

## 6. Work breakdown & effort

| # | Work item | Touch points |
|---|-----------|--------------|
| 1 | Thread pool + registry anchoring | `LuaEngine` |
| 2 | Callback dispatch via `NewThread`+`Resume` | `api.go: callLuaCallback`, `callPTYDataCallback` |
| 3 | `blim.sleep` as yield point | `api.go: registerSleepFunction` |
| 4 | `io.read` as yield point | `lua_engine.go: registerIOReadContextAwareInternal` |
| 5 | (optional) async `char.read/write` as yield points | `api.go: registerCharacteristicFunction` |
| 6 | Scheduler: run queue, timer wheel, completion routing | new file under `internal/lua` |
| 7 | Cancellation + shutdown integration | `LuaEngine.Close`, shutdown hooks, `scriptExecutionCtx` |
| 8 | Move/adapt `inCallbackCount` accounting across yields | `LuaEngine` / `api.go` |
| 9 | Tests: nested sleep, overlapping callbacks, cancel-during-sleep, shutdown-during-sleep, GC stress | `internal/lua/*_test.go` |

**Estimate:** ~2–4 weeks for an engineer fluent in this codebase to reach a working version, plus a hardening tail for race stabilization and coverage. **Risk: high** — it rewrites the most concurrency-sensitive code, which already carries `DANGEROUS`/SIGSEGV-handling comments in `callLuaCallback`.

For contrast: the `inCallbackCount` guard is ~1 hour; the deferral API is days.

---

## 7. Testing strategy

- **Regression (exists):** `internal/lua/api_test.go` → `TestErrorHandling` → "Lua: blim.sleep inside subscription callback raises guard error" — `blim.sleep` inside a callback must not crash. Under the guard it raises a clean error; under this RFC it must instead *suspend and complete* (this test's expectation is inverted when the scheduler lands).
- Nested/overlapping callbacks that each sleep; assert all complete and globals are consistent.
- `read_char(wait_ms)` inside a callback returns the key and does not corrupt state.
- Cancel (`scriptExecutionCtx`) while a callback is parked in `sleep`/`read` → clean unwind, no leak.
- `Close()`/shutdown while callbacks are parked → deterministic drain, no goroutine/thread-ref leak.
- GC stress: many short-lived parked threads; verify registry refs are released and no thread is collected while parked.
- Run the concurrency-sensitive suites under the Go race detector.

---

## 8. Open questions

1. `char.read/write` are guarded in the shipped fix (clean error inside a callback). In the scheduler, do we promote them to async yield points in v1, or keep them guarded and only unblock `sleep`/`io.read`/`read_char`?
2. Empirically confirm whether concurrent depth-2 reentrancy corrupts on the current build (a scratch reproduction), to validate that no cheaper path exists before committing to the scheduler.
3. Thread-pool sizing / backpressure when callbacks fire faster than they complete.
4. Interaction with the existing debug-hook-based cancellation used by `blim.sleep`.

---

## 9. Recommendation

Ship the `inCallbackCount` guard for issue #3 now (deterministic failure, no crash). Schedule **Variant 1** as a separate, funded effort when blocking-inside-callbacks becomes a real product requirement; until then, document the workaround (do BLE ops / waits from the main loop; callbacks only set flags/cache values).