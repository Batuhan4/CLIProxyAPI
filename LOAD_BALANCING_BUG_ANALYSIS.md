# Load Balancing Bug Analysis and Fix Guide

## Problem Statement

The round-robin load balancing for Gemini CLI, Codex, and Gemini API accounts is not working correctly. Users experience failures due to the same account being used repeatedly instead of proper round-robin distribution across multiple accounts.

## Root Cause

### Location
The bug exists in `sdk/cliproxy/auth/selector.go` in the `RoundRobinSelector.Pick()` method.

### Issue
The round-robin selector increments its cursor on **every call** to `Pick()`, but the `Manager.executeWithProvider()` method (in `sdk/cliproxy/auth/manager.go`) calls `Pick()` **multiple times per request** when trying different authentication credentials.

### Code Flow

1. **Request arrives** for a model (e.g., "gemini-2.0-flash-exp")
2. **Manager.Execute()** is called with a list of providers
3. For each provider, **Manager.executeWithProvider()** is called
4. Within executeWithProvider(), there's a **retry loop** that calls `pickNext()` multiple times:
   ```go
   for {
       auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
       // Try to execute with this auth
       // If it fails, loop continues and calls pickNext() again
   }
   ```
5. Each call to `pickNext()` calls **selector.Pick()**, which increments the cursor

### The Problem Illustrated

With 3 accounts [Account A, Account B, Account C]:

**Request 1:**
- `Pick()` call 1: cursor = 0 → 1, returns Account A, execution fails
- `Pick()` call 2: cursor = 1 → 2, returns Account B, execution fails  
- `Pick()` call 3: cursor = 2 → 3, returns Account C, execution fails
- Request fails after trying all accounts

**Request 2:**
- `Pick()` call 1: cursor = 3 → 4, returns `accounts[3 % 3]` = **Account A** (same as Request 1!)
- Same pattern repeats...

**Expected behavior:** Each new request should try a different starting account (Request 1 starts with A, Request 2 starts with B, Request 3 starts with C, etc.)

**Actual behavior:** The cursor advances by the number of failed attempts, causing the same account to be retried on subsequent requests.

## Solution

The round-robin cursor should only advance **once per request**, not once per retry attempt within a request.

### Approach 1: Increment After First Pick (Recommended)

Modify `RoundRobinSelector.Pick()` to only increment on the first successful pick within a request context, not on every call.

Add a mechanism to track whether this is the first pick for a given request. This can be done by:
1. Adding a `firstPick` flag to the context or options
2. Incrementing the cursor only when `len(tried)` is 0 (no auths have been tried yet)
3. Using a separate "already incremented" tracking mechanism

### Approach 2: Increment in Manager (Alternative)

Move the cursor advancement logic from the selector to the manager:
1. Remove cursor increment from `RoundRobinSelector.Pick()`
2. Add a new method `RoundRobinSelector.Advance(provider, model string)`
3. Call `selector.Advance()` from `Manager.executeWithProvider()` only once (not in the retry loop)

### Approach 3: Reset-Based (Simple but less elegant)

Track which auth was returned first for each request and only increment when we're back to selecting for a new request:
1. Store the "first auth ID" for each request
2. Only increment cursor when selecting an auth not in the "tried" map and this is a new request

## Recommended Fix

**Approach 1** is recommended as it requires minimal changes and keeps the logic encapsulated in the selector.

### Implementation Steps

1. **Modify `RoundRobinSelector.Pick()`** in `sdk/cliproxy/auth/selector.go`:
   - Check if this is the first pick for the request by examining the `tried` auths passed in (manager passes this)
   - Only increment cursor if no auths have been tried yet
   
2. **Code change:**
   ```go
   func (s *RoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
       // ... existing validation code ...
       
       // Make round-robin deterministic even if caller's candidate order is unstable.
       if len(available) > 1 {
           sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
       }
       
       key := provider + ":" + model
       s.mu.Lock()
       index := s.cursors[key]
       
       if index >= 2_147_483_640 {
           index = 0
       }
       
       // Only increment cursor on first pick (when no auths have been tried yet)
       // Note: This assumes the tried set is passed correctly from the caller
       s.cursors[key] = index + 1
       s.mu.Unlock()
       
       return available[index%len(available)], nil
   }
   ```

   Wait, this doesn't work because `Pick()` doesn't receive the `tried` map. We need to pass it through or use Approach 2.

## Better Solution: Track at Manager Level

Actually, looking at the code more carefully, the cleanest solution is:

1. **Remove cursor increment from `RoundRobinSelector.Pick()`**
2. **Add a new method** to advance the cursor after a request completes
3. **Call the advance method from Manager** only once per `executeWithProvider()` call

### Modified Implementation

**File: `sdk/cliproxy/auth/selector.go`**

Remove the increment from `Pick()`:
```go
func (s *RoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
    // ... existing code for filtering available auths ...
    
    key := provider + ":" + model
    s.mu.Lock()
    index := s.cursors[key]
    
    if index >= 2_147_483_640 {
        s.cursors[key] = 0
        index = 0
    }
    s.mu.Unlock()
    
    return available[index%len(available)], nil
}

// Advance moves the cursor forward for the given provider and model combination.
// This should be called once per request, after all retry attempts are complete.
func (s *RoundRobinSelector) Advance(provider, model string) {
    key := provider + ":" + model
    s.mu.Lock()
    s.cursors[key] = s.cursors[key] + 1
    if s.cursors[key] >= 2_147_483_640 {
        s.cursors[key] = 0
    }
    s.mu.Unlock()
}
```

**File: `sdk/cliproxy/auth/manager.go`**

Modify `executeWithProvider()` to advance cursor only once:
```go
func (m *Manager) executeWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
    if provider == "" {
        return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
    }
    
    // Advance round-robin cursor once per executeWithProvider call
    if rrs, ok := m.selector.(*RoundRobinSelector); ok && rrs != nil {
        defer rrs.Advance(provider, req.Model)
    }
    
    tried := make(map[string]struct{})
    var lastErr error
    for {
        auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
        // ... rest of existing code ...
    }
}
```

Apply the same pattern to `executeCountWithProvider()` and `executeStreamWithProvider()`.

## Testing the Fix

After implementing the fix, test with multiple accounts:

1. Set up 3+ accounts for the same provider (e.g., gemini-cli)
2. Send multiple sequential requests
3. Check logs to verify different accounts are used in round-robin order
4. Verify that when one account fails, the next request tries a different account first

### Expected Log Pattern (with 3 accounts: A, B, C)
```
Request 1: Try A → fail, Try B → fail, Try C → fail → Request fails
Request 2: Try B → success
Request 3: Try C → success
Request 4: Try A → success
Request 5: Try B → success
```

## Files to Modify

1. `sdk/cliproxy/auth/selector.go` - Fix the round-robin cursor increment logic
2. `sdk/cliproxy/auth/manager.go` - Add cursor advance calls at appropriate locations
3. (Optional) Add tests to verify round-robin behavior

## Additional Notes

- This bug affects ALL providers using the `RoundRobinSelector`: gemini, gemini-cli, codex, claude, qwen, iflow
- The provider-level rotation in `Manager` (the `providerOffsets` map) is working correctly; the bug is only in the auth-level selection within each provider
- The fix maintains backward compatibility and doesn't change the public API
