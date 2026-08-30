package prompt

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryRegisterRejectsDuplicateID(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)
	spec := PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     PromptRoleSystem,
		Template: "hello",
	}
	if err := reg.Register(spec); err != nil {
		t.Fatalf("register first spec: %v", err)
	}
	err := reg.Register(spec)
	if !errors.Is(err, ErrDuplicatePromptSpec) {
		t.Fatalf("expected ErrDuplicatePromptSpec, got %v", err)
	}
}

func TestRegistryRenderReturnsBaselinePrompt(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)
	if err := reg.Register(PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     PromptRoleSystem,
		Template: "hello {{ .Name }}",
	}); err != nil {
		t.Fatalf("register spec: %v", err)
	}

	out, err := reg.Render(context.Background(), "example.agent.system", Scope{}, map[string]any{
		"Name": "operator",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out == nil {
		t.Fatal("expected prompt content")
	}
	if out.Text != "hello operator" {
		t.Fatalf("unexpected render output: %q", out.Text)
	}
	if out.Ref.ID != "example.agent.system" {
		t.Fatalf("unexpected prompt ref id: %q", out.Ref.ID)
	}
	expectedVersion := VersionFromTemplate("hello {{ .Name }}")
	if out.Ref.Version != expectedVersion {
		t.Fatalf("unexpected prompt ref version: got %q want %q", out.Ref.Version, expectedVersion)
	}
}

func TestRegistryRenderBaselineWithFuncMap(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)
	if err := reg.Register(PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     PromptRoleSystem,
		Template: "hello {{ upper .Name }}",
		Funcs: map[string]any{
			"upper": strings.ToUpper,
		},
	}); err != nil {
		t.Fatalf("register spec: %v", err)
	}
	out, err := reg.Render(context.Background(), "example.agent.system", Scope{}, map[string]any{
		"Name": "operator",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out.Text != "hello OPERATOR" {
		t.Fatalf("unexpected render output: %q", out.Text)
	}
}

func TestRegistryRenderReturnsScopedOverride(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	reg := NewRegistry(store)
	if err := reg.Register(PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     PromptRoleSystem,
		Template: "baseline {{ .Name }}",
	}); err != nil {
		t.Fatalf("register spec: %v", err)
	}

	scope := Scope{
		SessionID: "sess_1",
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	}
	if err := store.Set(context.Background(), "example.agent.system", scope, "override {{ .Name }}", nil); err != nil {
		t.Fatalf("set override: %v", err)
	}

	out, err := reg.Render(context.Background(), "example.agent.system", scope, map[string]any{
		"Name": "operator",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out.Text != "override operator" {
		t.Fatalf("expected override render, got %q", out.Text)
	}
	expectedVersion := VersionFromTemplate("override {{ .Name }}")
	if out.Ref.Version != expectedVersion {
		t.Fatalf("unexpected override version: got %q want %q", out.Ref.Version, expectedVersion)
	}
}

func TestRegistryRenderRecorderOwnsResolvedEvents(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)
	if err := reg.Register(PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     PromptRoleSystem,
		Template: "hello",
		Version:  "v1",
	}); err != nil {
		t.Fatalf("register spec: %v", err)
	}
	recorder := NewRenderRecorder()
	scope := Scope{SessionID: "session", Labels: map[string]string{"site": "one"}}
	_, err := reg.Render(WithRenderRecorder(context.Background(), recorder), "example.agent.system", scope, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	scope.Labels["site"] = "changed"

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("expected one render event, got %d", len(events))
	}
	if events[0].PromptID != "example.agent.system" || events[0].Version != "v1" {
		t.Fatalf("unexpected render event: %#v", events[0])
	}
	if events[0].Scope.Labels["site"] != "one" {
		t.Fatalf("recorder retained caller-owned labels: %#v", events[0].Scope.Labels)
	}
	events[0].Scope.Labels["site"] = "returned copy"
	if recorder.Events()[0].Scope.Labels["site"] != "one" {
		t.Fatal("Events returned the recorder's label map")
	}
}

func TestRenderRecorderEventsHaveStableIdentityOrder(t *testing.T) {
	t.Parallel()

	recorder := NewRenderRecorder()
	recorder.record(RenderEvent{
		PromptID: "example.agent.user",
		Version:  "v2",
		Scope: Scope{
			SessionID: "session",
			Labels:    map[string]string{"site": "two"},
		},
	})
	recorder.record(RenderEvent{
		PromptID: "example.agent.system",
		Version:  "v1",
		Scope: Scope{
			SessionID: "session",
			Labels:    map[string]string{"site": "one"},
		},
	})
	recorder.record(RenderEvent{
		PromptID: "example.agent.system",
		Version:  "v1",
		Scope: Scope{
			SessionID: "session",
			Labels:    map[string]string{"site": "zero"},
		},
	})

	events := recorder.Events()
	want := []RenderEvent{
		{
			PromptID: "example.agent.system",
			Version:  "v1",
			Scope: Scope{
				SessionID: "session",
				Labels:    map[string]string{"site": "one"},
			},
		},
		{
			PromptID: "example.agent.system",
			Version:  "v1",
			Scope: Scope{
				SessionID: "session",
				Labels:    map[string]string{"site": "zero"},
			},
		},
		{
			PromptID: "example.agent.user",
			Version:  "v2",
			Scope: Scope{
				SessionID: "session",
				Labels:    map[string]string{"site": "two"},
			},
		},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("unexpected event order:\ngot:  %#v\nwant: %#v", events, want)
	}
}

func TestRegistryRenderInvalidOverrideTemplateFails(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	reg := NewRegistry(store)
	if err := reg.Register(PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     PromptRoleSystem,
		Template: "baseline {{ .Name }}",
	}); err != nil {
		t.Fatalf("register spec: %v", err)
	}
	scope := Scope{SessionID: "sess_1"}
	if err := store.Set(context.Background(), "example.agent.system", scope, "{{", nil); err != nil {
		t.Fatalf("set override: %v", err)
	}
	_, err := reg.Render(context.Background(), "example.agent.system", scope, map[string]any{"Name": "operator"})
	if !errors.Is(err, ErrTemplateParse) {
		t.Fatalf("expected ErrTemplateParse, got %v", err)
	}
}

func TestRegistryRenderReturnsNotFound(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)
	_, err := reg.Render(context.Background(), "missing.prompt", Scope{}, nil)
	if !errors.Is(err, ErrPromptNotFound) {
		t.Fatalf("expected ErrPromptNotFound, got %v", err)
	}
}
