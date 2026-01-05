Task: Introduce Lua API for Connection Lifecycle Management

Summary

Add connection lifecycle handling capabilities to the blim Lua API, enabling scripts to monitor connection state changes and perform reconnection
after expected disconnects (e.g., device reboot).

New API Functions

| Function                      | Signature              | Description                                               |
  |-------------------------------|------------------------|-----------------------------------------------------------|
| blim.connection_state()       | () → string            | Returns: "disconnected", "connecting", "connected"        |
| blim.on_connection_change(cb) | (function|nil)         | Register callback (state, reason), pass nil to unregister |
| blim.reconnect(timeout_ms)    | (number) → bool, error | Reconnect to same device, blocks until success or timeout |

Connection States

- disconnected - Not connected
- connecting - Connection attempt in progress
- connected - Ready for BLE operations

Callback Signature

blim.on_connection_change(function(state, reason)
-- state: "disconnected" | "connecting" | "connected"
-- reason: string | nil
end)

Acceptance Criteria

1. blim.connection_state() returns correct current state
2. blim.on_connection_change(cb) callback fires on state transitions
3. blim.on_connection_change(nil) unregisters the callback
4. blim.reconnect(timeout_ms) reconnects to same device address
5. Callback invocation is thread-safe (Go → Lua)

