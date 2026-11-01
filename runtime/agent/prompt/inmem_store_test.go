package prompt

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestInMemoryStoreResolveByPrecedence(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	ctx := context.Background()
	promptID := Ident("example.agent.system")

	if err := store.Set(ctx, promptID, Scope{}, "global", nil); err != nil {
		t.Fatalf("set global: %v", err)
	}
	if err := store.Set(ctx, promptID, Scope{Labels: map[string]string{"account": "acme"}}, "account", nil); err != nil {
		t.Fatalf("set account: %v", err)
	}
	if err := store.Set(ctx, promptID, Scope{Labels: map[string]string{"account": "acme", "region": "west"}}, "region", nil); err != nil {
		t.Fatalf("set region: %v", err)
	}
	if err := store.Set(ctx, promptID, Scope{SessionID: "sess_1", Labels: map[string]string{"account": "acme", "region": "west"}}, "session", nil); err != nil {
		t.Fatalf("set session: %v", err)
	}

	got, err := store.Resolve(ctx, promptID, Scope{
		SessionID: "sess_1",
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got == nil || got.Template != "session" {
		t.Fatalf("expected session override, got %#v", got)
	}
}

func TestInMemoryStoreResolveReturnsNilWhenMissing(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	got, err := store.Resolve(context.Background(), "missing", Scope{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil override, got %#v", got)
	}
}

func TestInMemoryStoreSetComputesVersion(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	if err := store.Set(context.Background(), "example.agent.system", Scope{}, "hello", nil); err != nil {
		t.Fatalf("set override: %v", err)
	}
	got, err := store.Resolve(context.Background(), "example.agent.system", Scope{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got == nil {
		t.Fatal("expected override")
	}
	if !strings.HasPrefix(got.Version, "sha256:") {
		t.Fatalf("expected sha256 version, got %q", got.Version)
	}
}

func TestInMemoryStoreHistoryNewestFirst(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	id := Ident("example.agent.system")
	if err := store.Set(context.Background(), id, Scope{}, "v1", nil); err != nil {
		t.Fatalf("set v1: %v", err)
	}
	time.Sleep(time.Millisecond)
	if err := store.Set(context.Background(), id, Scope{}, "v2", nil); err != nil {
		t.Fatalf("set v2: %v", err)
	}

	history, err := store.History(context.Background(), id)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected at least 2 history entries, got %d", len(history))
	}
	if history[0].Template != "v2" {
		t.Fatalf("expected newest first, got %q", history[0].Template)
	}
}

func TestInMemoryStoreReset(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	ctx := context.Background()
	promptID := Ident("example.agent.system")

	err := store.Reset(ctx, "", Scope{})
	if err == nil {
		t.Fatal("expected error for empty promptID")
	}

	scopeGlobal := Scope{}
	scopeAccount := Scope{Labels: map[string]string{"account": "acme"}}

	if err := store.Set(ctx, promptID, scopeGlobal, "global", nil); err != nil {
		t.Fatalf("set global: %v", err)
	}
	if err := store.Set(ctx, promptID, scopeAccount, "account", nil); err != nil {
		t.Fatalf("set account: %v", err)
	}

	got, err := store.Resolve(ctx, promptID, scopeAccount)
	if err != nil || got == nil || got.Template != "account" {
		t.Fatalf("expected account override, got %#v, err=%v", got, err)
	}

	if err := store.Reset(ctx, promptID, scopeAccount); err != nil {
		t.Fatalf("reset account: %v", err)
	}

	got, err = store.Resolve(ctx, promptID, scopeAccount)
	if err != nil || got == nil || got.Template != "global" {
		t.Fatalf("expected global override, got %#v, err=%v", got, err)
	}
}

func TestInMemoryStoreDeterministicTieBreaking(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	ctx := context.Background()
	promptID := Ident("example.agent.system")
	now := time.Now().UTC()

	store.mu.Lock()
	store.overrides = append(store.overrides,
		&Override{
			PromptID:  promptID,
			Scope:     Scope{Labels: map[string]string{"env": "dev"}},
			Template:  "dev template",
			CreatedAt: now,
		},
		&Override{
			PromptID:  promptID,
			Scope:     Scope{Labels: map[string]string{"env": "prod"}},
			Template:  "prod template",
			CreatedAt: now,
		},
	)
	store.mu.Unlock()

	reqScope := Scope{Labels: map[string]string{"env": "prod"}}
	got, err := store.Resolve(ctx, promptID, reqScope)
	if err != nil || got == nil || got.Template != "prod template" {
		t.Fatalf("expected prod template, got %#v, err=%v", got, err)
	}
}

func TestInMemoryStoreResetDottedKeysAndEmptyStringValues(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	ctx := context.Background()
	promptID := Ident("example.dotted")

	scopeDotted := Scope{
		Labels: map[string]string{
			"account.id": "123",
			"env":        "",
		},
	}

	if err := store.Set(ctx, promptID, scopeDotted, "dotted template", nil); err != nil {
		t.Fatalf("set dotted: %v", err)
	}

	got, err := store.Resolve(ctx, promptID, scopeDotted)
	if err != nil || got == nil || got.Template != "dotted template" {
		t.Fatalf("expected dotted override, got %#v, err=%v", got, err)
	}

	if err := store.Reset(ctx, promptID, scopeDotted); err != nil {
		t.Fatalf("reset dotted: %v", err)
	}

	got, err = store.Resolve(ctx, promptID, scopeDotted)
	if err != nil || got != nil {
		t.Fatalf("expected nil after reset, got %#v, err=%v", got, err)
	}
}
