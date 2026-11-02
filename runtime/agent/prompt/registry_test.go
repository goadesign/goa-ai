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

func TestRegistryStaticFieldValidation(t *testing.T) {
	t.Parallel()

	type customerData struct {
		Name string
	}
	type itemData struct {
		ID string
	}
	type validationData struct {
		Company  string
		Customer customerData
		Items    []itemData
	}
	argsType := reflect.TypeFor[validationData]()

	// Valid root field
	reg := NewRegistry(nil)
	err := reg.Register(PromptSpec{
		ID:       "valid.root",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Hello {{ .Company }}",
		ArgsType: argsType,
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	// Valid nested field
	err = reg.Register(PromptSpec{
		ID:       "valid.nested",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Customer: {{ .Customer.Name }}",
		ArgsType: argsType,
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	// Undeclared root field
	err = reg.Register(PromptSpec{
		ID:       "invalid.root",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Hello {{ .Undeclared }}",
		ArgsType: argsType,
	})
	if !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound, got %v", err)
	}

	// Undeclared nested field
	err = reg.Register(PromptSpec{
		ID:       "invalid.nested",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Customer: {{ .Customer.Unknown }}",
		ArgsType: argsType,
	})
	if !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound, got %v", err)
	}

	// With block rebinding dot does not check sub-fields as root
	err = reg.Register(PromptSpec{
		ID:       "valid.with",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "{{ with .Customer }}Name: {{ .Name }}{{ end }}",
		ArgsType: argsType,
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	// Range block rebinding dot does not check element fields as root
	err = reg.Register(PromptSpec{
		ID:       "valid.range",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "{{ range .Items }}Item: {{ .ID }}{{ end }}",
		ArgsType: argsType,
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	// ValidateOverride enforces fields
	err = reg.ValidateOverride("valid.root", "New template {{ .Unknown }}")
	if !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound for override, got %v", err)
	}

	err = reg.ValidateOverride("valid.root", "New template {{ .Company }}")
	if err != nil {
		t.Fatalf("unexpected error validating valid override: %v", err)
	}

	// Register and SetOverride reject undefined local named templates without mutating active store content.
	store := NewInMemoryStore()
	scopedReg := NewRegistry(store)
	if err := scopedReg.Register(PromptSpec{
		ID:       "orders.refund.system",
		AgentID:  "orders.refund",
		Role:     PromptRoleSystem,
		Template: "Baseline system prompt.",
		ArgsType: reflect.TypeFor[struct{}](),
	}); err != nil {
		t.Fatalf("register orders.refund.system: %v", err)
	}
	if err := scopedReg.Register(PromptSpec{
		ID:       "invalid.missing.template",
		AgentID:  "orders.refund",
		Role:     PromptRoleSystem,
		Template: `Hello {{ template "missing" }}.`,
		ArgsType: reflect.TypeFor[struct{}](),
	}); !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound for undefined template at Register, got %v", err)
	}
	if err := scopedReg.SetOverride(context.Background(), "orders.refund.system", Scope{}, `Hello {{ template "missing" }}.`, nil); !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound for undefined template at SetOverride, got %v", err)
	}
	rendered, err := scopedReg.Render(context.Background(), "orders.refund.system", Scope{}, struct{}{})
	if err != nil {
		t.Fatalf("render after rejected SetOverride: %v", err)
	}
	if rendered.Text != "Baseline system prompt." {
		t.Fatalf("expected baseline text to remain active after rejected SetOverride, got %q", rendered.Text)
	}
	if err := scopedReg.SetOverride(context.Background(), "orders.refund.system", Scope{}, `{{ define "sub" }}Updated{{ end }}{{ template "sub" . }} prompt.`, nil); err != nil {
		t.Fatalf("expected valid local named template override to succeed, got %v", err)
	}
	rendered, err = scopedReg.Render(context.Background(), "orders.refund.system", Scope{}, struct{}{})
	if err != nil {
		t.Fatalf("render after valid local named template override: %v", err)
	}
	if rendered.Text != "Updated prompt." {
		t.Fatalf("unexpected rendered text: %q", rendered.Text)
	}
}

func TestRegistrySubPromptComposition(t *testing.T) {
	t.Parallel()

	recorder := NewRenderRecorder()
	store := NewInMemoryStore()
	reg := NewRegistry(store)

	// Register sub-prompt
	if err := reg.Register(PromptSpec{
		ID:       "sub.guardrails",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Guardrails for {{ .Name }}",
	}); err != nil {
		t.Fatalf("register sub spec: %v", err)
	}

	// Register main prompt that renders sub-prompt
	if err := reg.Register(PromptSpec{
		ID:       "main.system",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "System: {{ render \"sub.guardrails\" . }}",
	}); err != nil {
		t.Fatalf("register main spec: %v", err)
	}

	ctx := WithRenderRecorder(context.Background(), recorder)
	content, err := reg.Render(ctx, "main.system", Scope{}, map[string]any{"Name": "Alice"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if content.Text != "System: Guardrails for Alice" {
		t.Fatalf("unexpected text: %q", content.Text)
	}
	if content.Ref.ID != "main.system" {
		t.Fatalf("unexpected root ref id: %q", content.Ref.ID)
	}
	if len(content.Refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(content.Refs))
	}
	if content.Refs[0].ID != "main.system" || content.Refs[1].ID != "sub.guardrails" {
		t.Fatalf("unexpected content.Refs: %#v", content.Refs)
	}

	// Check recorder recorded both events in stable identity order
	events := recorder.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 recorded events, got %d", len(events))
	}
	if events[0].PromptID != "main.system" || events[1].PromptID != "sub.guardrails" {
		t.Fatalf("unexpected recorded events order: %#v", events)
	}
}

func TestRegistryCycleDetection(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)

	// Direct self-recursion
	if err := reg.Register(PromptSpec{
		ID:       "self.cycle",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "{{ render \"self.cycle\" . }}",
	}); err != nil {
		t.Fatalf("register self cycle: %v", err)
	}

	_, err := reg.Render(context.Background(), "self.cycle", Scope{}, nil)
	if !errors.Is(err, ErrTemplateCycle) {
		t.Fatalf("expected ErrTemplateCycle for self recursion, got %v", err)
	}

	// Mutual recursion: A -> B -> A
	if err := reg.Register(PromptSpec{
		ID:       "mutual.a",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "{{ render \"mutual.b\" . }}",
	}); err != nil {
		t.Fatalf("register mutual.a: %v", err)
	}
	if err := reg.Register(PromptSpec{
		ID:       "mutual.b",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "{{ render \"mutual.a\" . }}",
	}); err != nil {
		t.Fatalf("register mutual.b: %v", err)
	}

	_, err = reg.Render(context.Background(), "mutual.a", Scope{}, nil)
	if !errors.Is(err, ErrTemplateCycle) {
		t.Fatalf("expected ErrTemplateCycle for mutual recursion, got %v", err)
	}
}

func TestRegistryManagementMethods(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewInMemoryStore()
	reg := NewRegistry(store)

	spec := PromptSpec{
		ID:       "example.prompt",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "hello {{ .Name }}",
	}
	if err := reg.Register(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Spec
	retrieved, ok := reg.Spec("example.prompt")
	if !ok || retrieved.ID != "example.prompt" {
		t.Fatalf("Spec failed: %v, %v", retrieved, ok)
	}
	_, ok = reg.Spec("nonexistent")
	if ok {
		t.Fatal("expected false for nonexistent spec")
	}

	// SetOverride
	scope := Scope{SessionID: "sess_1"}
	err := reg.SetOverride(ctx, "example.prompt", scope, "custom {{ .Name }}", map[string]string{"env": "test"})
	if err != nil {
		t.Fatalf("SetOverride failed: %v", err)
	}

	// Resolve with override
	resSpec, resOverride, err := reg.Resolve(ctx, "example.prompt", scope)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if resSpec.ID != "example.prompt" || resOverride == nil || resOverride.Template != "custom {{ .Name }}" {
		t.Fatalf("unexpected resolve result: spec=%#v, override=%#v", resSpec, resOverride)
	}

	// History
	history, err := reg.History(ctx, "example.prompt")
	if err != nil {
		t.Fatalf("History failed: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(history))
	}

	// ResetOverride
	if err := reg.ResetOverride(ctx, "example.prompt", scope); err != nil {
		t.Fatalf("ResetOverride failed: %v", err)
	}

	// Resolve after reset
	_, resOverride, err = reg.Resolve(ctx, "example.prompt", scope)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if resOverride != nil {
		t.Fatalf("expected nil override after reset, got %#v", resOverride)
	}

	// Operations on registry without store
	noStoreReg := NewRegistry(nil)
	if err := noStoreReg.Register(spec); err != nil {
		t.Fatalf("register on noStoreReg: %v", err)
	}
	err = noStoreReg.SetOverride(ctx, "example.prompt", scope, "tmpl", nil)
	if !errors.Is(err, ErrPromptStoreNotConfigured) {
		t.Fatalf("expected ErrPromptStoreNotConfigured, got %v", err)
	}
	err = noStoreReg.ResetOverride(ctx, "example.prompt", scope)
	if !errors.Is(err, ErrPromptStoreNotConfigured) {
		t.Fatalf("expected ErrPromptStoreNotConfigured, got %v", err)
	}
	_, err = noStoreReg.History(ctx, "example.prompt")
	if !errors.Is(err, ErrPromptStoreNotConfigured) {
		t.Fatalf("expected ErrPromptStoreNotConfigured, got %v", err)
	}
}

func TestRegistryAtomicRenderEventPublication(t *testing.T) {
	t.Parallel()

	recorder := NewRenderRecorder()
	store := NewInMemoryStore()
	reg := NewRegistry(store)

	if err := reg.Register(PromptSpec{
		ID:       "child.prompt",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Child rendered",
	}); err != nil {
		t.Fatalf("register child: %v", err)
	}

	if err := reg.Register(PromptSpec{
		ID:       "failing.main",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Main: {{ render \"child.prompt\" . }} {{ .MissingField }}",
	}); err != nil {
		t.Fatalf("register main: %v", err)
	}

	ctx := WithRenderRecorder(context.Background(), recorder)
	type dataStruct struct {
		Existing string
	}
	_, err := reg.Render(ctx, "failing.main", Scope{}, dataStruct{Existing: "ok"})
	if err == nil {
		t.Fatal("expected render error on missing field")
	}

	events := recorder.Events()
	if len(events) != 0 {
		t.Fatalf("expected 0 recorded events on failed render, got %d: %#v", len(events), events)
	}
}
