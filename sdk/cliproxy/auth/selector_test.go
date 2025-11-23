package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestRoundRobinSelector_AdvanceOnlyOnce verifies that the round-robin cursor
// only advances once per request, not once per retry attempt.
func TestRoundRobinSelector_AdvanceOnlyOnce(t *testing.T) {
	selector := &RoundRobinSelector{}
	provider := "gemini-cli"
	model := "gemini-2.0-flash-exp"

	// Create 3 test auth entries
	auths := []*Auth{
		{ID: "auth-1", Provider: provider, Status: StatusActive},
		{ID: "auth-2", Provider: provider, Status: StatusActive},
		{ID: "auth-3", Provider: provider, Status: StatusActive},
	}

	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	// Request 1: Should pick auth-1 (index 0)
	auth1, err := selector.Pick(ctx, provider, model, opts, auths)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth1.ID != "auth-1" {
		t.Errorf("Request 1, attempt 1: expected auth-1, got %s", auth1.ID)
	}

	// Simulate retry - should pick auth-1 (cursor should not advance within same request)
	auth2, err := selector.Pick(ctx, provider, model, opts, auths)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth2.ID != "auth-1" {
		t.Errorf("Request 1, attempt 2: expected auth-1 (cursor should not advance), got %s", auth2.ID)
	}

	// Manually advance cursor to simulate end of request
	selector.Advance(provider, model)

	// Request 2: Should pick auth-2 (index 1)
	auth3, err := selector.Pick(ctx, provider, model, opts, auths)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth3.ID != "auth-2" {
		t.Errorf("Request 2, attempt 1: expected auth-2, got %s", auth3.ID)
	}

	// Simulate retry - should still pick auth-2
	auth4, err := selector.Pick(ctx, provider, model, opts, auths)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth4.ID != "auth-2" {
		t.Errorf("Request 2, attempt 2: expected auth-2 (cursor should not advance), got %s", auth4.ID)
	}

	// Manually advance cursor to simulate end of request
	selector.Advance(provider, model)

	// Request 3: Should pick auth-3 (index 2)
	auth5, err := selector.Pick(ctx, provider, model, opts, auths)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth5.ID != "auth-3" {
		t.Errorf("Request 3: expected auth-3, got %s", auth5.ID)
	}

	// Manually advance cursor
	selector.Advance(provider, model)

	// Request 4: Should wrap around to auth-1 (index 3 % 3 = 0)
	auth6, err := selector.Pick(ctx, provider, model, opts, auths)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth6.ID != "auth-1" {
		t.Errorf("Request 4: expected auth-1 (wrap around), got %s", auth6.ID)
	}
}

// TestRoundRobinSelector_PerProviderModel verifies that cursors are independent
// per provider:model combination.
func TestRoundRobinSelector_PerProviderModel(t *testing.T) {
	selector := &RoundRobinSelector{}

	auths1 := []*Auth{
		{ID: "gemini-1", Provider: "gemini", Status: StatusActive},
		{ID: "gemini-2", Provider: "gemini", Status: StatusActive},
	}

	auths2 := []*Auth{
		{ID: "claude-1", Provider: "claude", Status: StatusActive},
		{ID: "claude-2", Provider: "claude", Status: StatusActive},
	}

	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	// Pick from gemini
	auth1, err := selector.Pick(ctx, "gemini", "gemini-2.0", opts, auths1)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth1.ID != "gemini-1" {
		t.Errorf("Expected gemini-1, got %s", auth1.ID)
	}

	// Pick from claude - should start at index 0 (independent cursor)
	auth2, err := selector.Pick(ctx, "claude", "claude-3", opts, auths2)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth2.ID != "claude-1" {
		t.Errorf("Expected claude-1, got %s", auth2.ID)
	}

	// Advance gemini cursor
	selector.Advance("gemini", "gemini-2.0")

	// Pick from gemini again - should be gemini-2
	auth3, err := selector.Pick(ctx, "gemini", "gemini-2.0", opts, auths1)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth3.ID != "gemini-2" {
		t.Errorf("Expected gemini-2, got %s", auth3.ID)
	}

	// Pick from claude again - should still be claude-1 (cursor not advanced)
	auth4, err := selector.Pick(ctx, "claude", "claude-3", opts, auths2)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if auth4.ID != "claude-1" {
		t.Errorf("Expected claude-1, got %s", auth4.ID)
	}
}
