# MANDATORY: Test Standards

## 1. Test Uniqueness

**CRITICAL:** Every test MUST verify unique behavior. NEVER create duplicate or similar tests without serious justification.

**MUST:**
- Verify each test covers distinct behavior
- Document justification in GOAL if tests appear similar
- Consolidate overlapping test cases

**Example justification in GOAL:**
```go
// GOAL: Verify subscription error handling with network timeout
//       (separate from TestSubscriptionInvalidUUID which device_test validation errors)
```

## 2. Test Documentation Format

**CRITICAL:** Every test MUST use this format:

```go
func TestName(t *testing.T) {
    // GOAL: [What behavior is being verified]
    //
    // TEST SCENARIO: [Action] → [Expected Result] → [Verification]
}
```

**MUST:**
- Focus on behavior, not implementation
- Use arrow notation (→) in TEST SCENARIO
- Keep TEST SCENARIO to one line

**NEVER:**
- Describe code execution steps
- Mention function names in documentation
- Use numbered lists

## 3. Deterministic Assertions

**CRITICAL:** Tests MUST use unconditional assertions. NEVER allow pass/fail to depend on conditional logic.

### ❌ FORBIDDEN: Conditional Assertions

```go
// ❌ WRONG: Test passes if err is nil (no verification!)
if err != nil {
    assert.Error(t, err)
}

// ❌ WRONG: Lua with conditional assertions
script := `
    if err ~= nil then
        assert(value == nil)
    end
`
```

**Why forbidden:** Creates code paths where ZERO assertions execute, allowing tests to pass without verification.

### ✅ REQUIRED: Unconditional Assertions

```go
// ✅ CORRECT: All assertions MUST execute
assert.Error(t, err, "MUST fail on invalid input")
assert.Nil(t, result, "result MUST be nil on error")

// ✅ CORRECT: Lua with unconditional assertions
script := `
    assert(err ~= nil, "MUST fail on invalid operation")
    assert(value == nil, "value MUST be nil on error")
`
```

## Mandatory Checklist

EVERY test MUST:

1. **Define expected outcome** - Success OR failure, never "maybe"
2. **Assert outcome first** - Primary assertion BEFORE details
3. **Execute all assertions** - NEVER use `if` to skip assertions
4. **Set up explicit state** - Create error conditions deliberately

## Complete Example

```go
func TestBLESubscriptionError(t *testing.T) {
    // GOAL: Verify subscription fails with clear error on invalid UUID
    //
    // TEST SCENARIO: Subscribe with invalid UUID → error returned → value is nil

    api, mockConn, L := setupBLEAPI2Test(t)
    
    // Explicitly create error condition
    mockConn.On("SubscribeLua", mock.Anything).Return(errors.New("invalid UUID"))
    
    script := `
        local result, err = ble.subscribe({
            service = "invalid-uuid",
            char = "test"
        })
        
        -- All assertions MUST execute
        assert(err ~= nil, "MUST fail on invalid UUID")
        assert(result == nil, "result MUST be nil on error")
        assert(type(err) == "string", "error MUST be string")
    `
    
    err := L.DoString(script)
    assert.NoError(t, err, "Lua script MUST execute")
    mockConn.AssertExpectations(t)
}
```

## Enforcement

- ALL rules are **NON-NEGOTIABLE**
- **ALWAYS** verify tests follow both standards
- **REJECT** tests that violate either rule