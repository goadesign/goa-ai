package prompt

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"
	"text/template"
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

func TestRegistryStaticFieldValidationAST(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)

	// Untyped manual prompt (spec.ArgsType == nil): accepts arbitrary fields
	if err := reg.Register(PromptSpec{
		ID:       "untyped.prompt",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Hello {{ .arbitrary.deep.field }}",
	}); err != nil {
		t.Fatalf("untyped prompt register: %v", err)
	}

	// Zero-arg prompt (ArgsType: struct{}): static text succeeds, field access fails
	if err := reg.Register(PromptSpec{
		ID:       "zeroarg.valid",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Static instructions with no fields",
		ArgsType: reflect.TypeFor[struct{}](),
	}); err != nil {
		t.Fatalf("zero-arg static text register: %v", err)
	}
	err := reg.Register(PromptSpec{
		ID:       "zeroarg.invalid",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "Tries to access {{ .foo }}",
		ArgsType: reflect.TypeFor[struct{}](),
	})
	if !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound for zeroarg field access, got %v", err)
	}

	type astCustomer struct {
		Name string
		Dict map[string]string
	}
	type astItem struct {
		ID string
	}
	type astStructured struct {
		Key string
	}
	type astData struct {
		Customer   astCustomer
		Items      []astItem
		Config     map[string]any
		Structured astStructured
	}
	schemaArgsType := reflect.TypeFor[astData]()

	cases := []struct {
		name    string
		tmpl    string
		wantErr bool
	}{
		{"var assignment valid", "{{ $x := .Customer }}{{ $x.Name }}", false},
		{"var assignment undeclared", "{{ $x := .Customer }}{{ $x.Invalid }}", true},
		{"root var valid", "{{ $.Customer.Name }}", false},
		{"root var undeclared", "{{ $.Customer.Invalid }}", true},
		{"index config valid", `{{ index .Config "setting" }}`, false},
		{"index customer dict valid", `{{ index .Customer.Dict "Name" }}`, false},
		{"index items valid", `{{ index .Items 0 }}`, false},
		{"index undeclared", `{{ (index .Items 0).Invalid }}`, true},
		{"with var valid", "{{ with $c := .Customer }}{{ $c.Name }}{{ end }}", false},
		{"with var undeclared", "{{ with $c := .Customer }}{{ $c.Invalid }}{{ end }}", true},
		{"range 1 var valid", "{{ range $it := .Items }}{{ $it.ID }}{{ end }}", false},
		{"range 1 var undeclared", "{{ range $it := .Items }}{{ $it.Invalid }}{{ end }}", true},
		{"range 2 var valid", "{{ range $idx, $it := .Items }}{{ $it.ID }}{{ end }}", false},
		{"range 2 var undeclared", "{{ range $idx, $it := .Items }}{{ $it.Invalid }}{{ end }}", true},
		{"chain valid", "{{ (.Customer).Name }}", false},
		{"chain undeclared", "{{ (.Customer).Invalid }}", true},
		{"named template valid", `{{ define "card" }}{{ .Name }}{{ end }}{{ template "card" .Customer }}`, false},
		{"named template undeclared", `{{ define "card" }}{{ .Invalid }}{{ end }}{{ template "card" .Customer }}`, true},
		{"unstructured object deep valid", "{{ .Config.nested.setting.flag }}", false},
		{"structured object undeclared", "{{ .Structured.Invalid }}", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := reg.Register(PromptSpec{
				ID:       Ident("test." + tc.name),
				AgentID:  "agent",
				Role:     PromptRoleSystem,
				Template: tc.tmpl,
				ArgsType: schemaArgsType,
			})
			if tc.wantErr {
				if !errors.Is(err, ErrTemplateFieldNotFound) {
					t.Fatalf("expected ErrTemplateFieldNotFound, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestRegistryDeduplicatePromptRefs(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)
	if err := reg.Register(PromptSpec{
		ID:       "child.sub",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: "child output",
		Version:  "v1",
	}); err != nil {
		t.Fatalf("register child: %v", err)
	}

	if err := reg.Register(PromptSpec{
		ID:       "main.dup",
		AgentID:  "agent",
		Role:     PromptRoleSystem,
		Template: `{{ render "child.sub" . }} {{ render "child.sub" . }}`,
		Version:  "v1",
	}); err != nil {
		t.Fatalf("register main: %v", err)
	}

	content, err := reg.Render(context.Background(), "main.dup", Scope{}, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if len(content.Refs) != 2 {
		t.Fatalf("expected 2 unique refs, got %d: %#v", len(content.Refs), content.Refs)
	}
	if content.Refs[0].ID != "main.dup" || content.Refs[1].ID != "child.sub" {
		t.Fatalf("unexpected refs: %#v", content.Refs)
	}
}

func TestRegistryServiceLevelAndSpecValidation(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)

	// Service-level prompt with empty AgentID succeeds
	err := reg.Register(PromptSpec{
		ID:       "service.shared.prompt",
		AgentID:  "", // Service-level / shared prompt
		Role:     PromptRoleSystem,
		Template: "service level prompt",
	})
	if err != nil {
		t.Fatalf("expected service-level prompt registration to succeed, got %v", err)
	}

	// Empty ID fails
	err = reg.Register(PromptSpec{
		Role:     PromptRoleSystem,
		Template: "hello",
	})
	if !errors.Is(err, ErrInvalidPromptSpec) {
		t.Fatalf("expected ErrInvalidPromptSpec for empty ID, got %v", err)
	}

	// Empty Role fails
	err = reg.Register(PromptSpec{
		ID:       "bad.role",
		Template: "hello",
	})
	if !errors.Is(err, ErrInvalidPromptSpec) {
		t.Fatalf("expected ErrInvalidPromptSpec for empty Role, got %v", err)
	}

	// Empty Template fails
	err = reg.Register(PromptSpec{
		ID:   "bad.template",
		Role: PromptRoleSystem,
	})
	if !errors.Is(err, ErrInvalidPromptSpec) {
		t.Fatalf("expected ErrInvalidPromptSpec for empty Template, got %v", err)
	}
}

func TestRegistryValidateSpecs(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil)
	if err := reg.Register(PromptSpec{
		ID:       "already.registered",
		Role:     PromptRoleSystem,
		Template: "existing",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Valid slice of specs
	specs := []PromptSpec{
		{
			ID:       "new.spec.1",
			Role:     PromptRoleSystem,
			Template: "first",
		},
		{
			ID:       "new.spec.2",
			Role:     PromptRoleUser,
			Template: "second",
		},
	}
	if err := reg.ValidateSpecs(specs); err != nil {
		t.Fatalf("expected ValidateSpecs to succeed, got %v", err)
	}
	// Verify ValidateSpecs did not mutate reg
	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 registered spec, got %d", len(reg.List()))
	}

	// Duplicate ID within specs slice
	dupSpecs := []PromptSpec{
		{
			ID:       "duplicate.in.input",
			Role:     PromptRoleSystem,
			Template: "first",
		},
		{
			ID:       "duplicate.in.input",
			Role:     PromptRoleUser,
			Template: "second",
		},
	}
	err := reg.ValidateSpecs(dupSpecs)
	if !errors.Is(err, ErrDuplicatePromptSpec) {
		t.Fatalf("expected ErrDuplicatePromptSpec for duplicate in input, got %v", err)
	}

	// Conflict with already registered spec
	conflictSpecs := []PromptSpec{
		{
			ID:       "already.registered",
			Role:     PromptRoleSystem,
			Template: "duplicate of existing",
		},
	}
	err = reg.ValidateSpecs(conflictSpecs)
	if !errors.Is(err, ErrDuplicatePromptSpec) {
		t.Fatalf("expected ErrDuplicatePromptSpec for conflict with registry, got %v", err)
	}

	// Invalid spec inside slice
	invalidSpecs := []PromptSpec{
		{
			ID:       "invalid.spec",
			Role:     "",
			Template: "no role",
		},
	}
	err = reg.ValidateSpecs(invalidSpecs)
	if !errors.Is(err, ErrInvalidPromptSpec) {
		t.Fatalf("expected ErrInvalidPromptSpec for invalid spec in slice, got %v", err)
	}
}

func TestValidatePromptTemplateDirect(t *testing.T) {
	t.Parallel()

	type directData struct {
		Name string
	}
	spec := PromptSpec{
		ID:       "direct.test",
		Role:     PromptRoleSystem,
		ArgsType: reflect.TypeFor[directData](),
	}

	if err := ValidatePromptTemplate(spec, "Hello {{ .Name }}"); err != nil {
		t.Fatalf("expected valid template to pass, got %v", err)
	}

	if err := ValidatePromptTemplate(spec, "Hello {{ .Missing }}"); !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound, got %v", err)
	}
}

func TestAdversarialReviewA62(t *testing.T) {
	t.Parallel()

	type addressStruct struct {
		City string
	}
	type customerStruct struct {
		Name    string
		Address addressStruct
	}
	type adversarialRoot struct {
		Customer customerStruct
	}
	type returnedStruct struct {
		Label string
	}

	spec := PromptSpec{
		ID:       "adversarial.review",
		Role:     PromptRoleSystem,
		ArgsType: reflect.TypeFor[adversarialRoot](),
		Funcs: template.FuncMap{
			"decorate": func(any) map[string]any {
				return map[string]any{"Label": "VIP"}
			},
			"name": func(any) string {
				return "acme"
			},
			"typed": func(any) returnedStruct {
				return returnedStruct{Label: "VIP"}
			},
		},
	}

	cases := []struct {
		name      string
		template  string
		wantError bool
	}{
		{
			name:      "if_alias_typo",
			template:  "{{if $c := .Customer}}{{$c.Missing}}{{end}}",
			wantError: true,
		},
		{
			name:      "valid_if_alias",
			template:  "{{if $c := .Customer}}{{$c.Name}}{{end}}",
			wantError: false,
		},
		{
			name:      "valid_reassignment",
			template:  "{{$x := .Customer}}{{$x = $x.Address}}{{$x.City}}",
			wantError: false,
		},
		{
			name:      "invalid_reassignment_typo",
			template:  "{{$x := .Customer}}{{$x = $x.Missing}}",
			wantError: true,
		},
		{
			name:      "valid_function_pipeline_map",
			template:  "{{with decorate .Customer}}{{.Label}}{{end}}",
			wantError: false,
		},
		{
			name:      "invalid_function_pipeline_string",
			template:  "{{with name .Customer}}{{.Label}}{{end}}",
			wantError: true,
		},
		{
			name:      "struct_function_valid_field",
			template:  "{{with typed .Customer}}{{.Label}}{{end}}",
			wantError: false,
		},
		{
			name:      "struct_function_invalid_field",
			template:  "{{with typed .Customer}}{{.Missing}}{{end}}",
			wantError: true,
		},
		{
			name:      "chain_node_function_missing_field",
			template:  "{{(name .Customer).Missing}}",
			wantError: true,
		},
		{
			name:      "chain_node_struct_function_valid",
			template:  "{{(typed .Customer).Label}}",
			wantError: false,
		},
		{
			name:      "chain_node_struct_function_missing",
			template:  "{{(typed .Customer).Missing}}",
			wantError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePromptTemplate(spec, tc.template)
			if tc.wantError {
				if !errors.Is(err, ErrTemplateFieldNotFound) {
					t.Fatalf("expected ErrTemplateFieldNotFound, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected template to be valid, got %v", err)
				}
			}
		})
	}
}

func TestRegistryRecursiveType(t *testing.T) {
	t.Parallel()

	type recursiveNode struct {
		Name string
		Next *recursiveNode
	}

	spec := PromptSpec{
		ID:       "recursive.prompt",
		Role:     PromptRoleSystem,
		ArgsType: reflect.TypeFor[*recursiveNode](),
	}

	// accepts .Name, .Next.Name, .Next.Next.Name
	valid := []string{
		"Hello {{ .Name }}",
		"Hello {{ .Next.Name }}",
		"Hello {{ .Next.Next.Name }}",
		`{{ define "visit" }}{{ .Name }}{{ if .Next }}{{ template "visit" .Next }}{{ end }}{{ end }}{{ template "visit" . }}`,
	}
	for _, src := range valid {
		if err := ValidatePromptTemplate(spec, src); err != nil {
			t.Fatalf("expected valid recursive template %q to pass, got: %v", src, err)
		}
	}

	// rejects .Next.Missing and .Next.Next.Missing
	invalid := []string{
		"Hello {{ .Next.Missing }}",
		"Hello {{ .Next.Next.Missing }}",
	}
	for _, src := range invalid {
		if err := ValidatePromptTemplate(spec, src); !errors.Is(err, ErrTemplateFieldNotFound) {
			t.Fatalf("expected ErrTemplateFieldNotFound for %q, got: %v", src, err)
		}
	}
}

func TestRegistryClosedEmptyStruct(t *testing.T) {
	t.Parallel()

	spec := PromptSpec{
		ID:       "empty.struct.prompt",
		Role:     PromptRoleSystem,
		ArgsType: reflect.TypeFor[struct{}](),
	}

	if err := ValidatePromptTemplate(spec, "static text only"); err != nil {
		t.Fatalf("expected static text to pass, got: %v", err)
	}

	if err := ValidatePromptTemplate(spec, "tries to access {{ .Missing }}"); !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound, got: %v", err)
	}
}

func TestRegistryScalarCollections(t *testing.T) {
	t.Parallel()

	type collectionsData struct {
		Names  []string
		Labels map[string]string
		Meta   map[string]any
	}

	spec := PromptSpec{
		ID:       "collections.prompt",
		Role:     PromptRoleSystem,
		ArgsType: reflect.TypeFor[collectionsData](),
	}

	valid := []string{
		"{{ index .Names 0 }}",
		"{{ .Labels.foo }}",
		"{{ .Meta.foo.bar }}",
	}
	for _, src := range valid {
		if err := ValidatePromptTemplate(spec, src); err != nil {
			t.Fatalf("expected %q to pass, got: %v", src, err)
		}
	}

	invalid := []string{
		"{{ (index .Names 0).Missing }}",
		"{{ .Labels.foo.Missing }}",
	}
	for _, src := range invalid {
		if err := ValidatePromptTemplate(spec, src); !errors.Is(err, ErrTemplateFieldNotFound) {
			t.Fatalf("expected ErrTemplateFieldNotFound for %q, got: %v", src, err)
		}
	}
}

func TestRegistryNamedTemplateMultipleTypes(t *testing.T) {
	t.Parallel()

	type typeA struct {
		OnlyOnA string
	}
	type typeB struct {
		Other string
	}
	type twoTypesData struct {
		A typeA
		B typeB
	}

	spec := PromptSpec{
		ID:       "multi.types.prompt",
		Role:     PromptRoleSystem,
		ArgsType: reflect.TypeFor[twoTypesData](),
	}

	tmpl := `{{ define "sub" }}{{ .OnlyOnA }}{{ end }}{{ template "sub" .A }}{{ template "sub" .B }}`
	if err := ValidatePromptTemplate(spec, tmpl); !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound when sub called with typeB, got: %v", err)
	}
}

func TestRegistryRenderRejectsInvalidRawOverrideWithArgsType(t *testing.T) {
	t.Parallel()

	type promptArgs struct {
		ValidField string
	}
	store := NewInMemoryStore()
	reg := NewRegistry(store)
	id := Ident("example.raw.override")
	spec := PromptSpec{
		ID:       id,
		Role:     PromptRoleSystem,
		Template: "Hello {{ .ValidField }}",
		ArgsType: reflect.TypeFor[promptArgs](),
	}
	if err := reg.Register(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	rawTemplate := "Dead branch: {{ if false }}{{ .Missing }}{{ end }}"
	if err := store.Set(context.Background(), id, Scope{}, rawTemplate, nil); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	_, err := reg.Render(context.Background(), id, Scope{}, promptArgs{ValidField: "ok"})
	if !errors.Is(err, ErrTemplateFieldNotFound) {
		t.Fatalf("expected ErrTemplateFieldNotFound for raw override, got: %v", err)
	}
}

type (
	// ReviewPerson is a test helper for template semantics verification.
	ReviewPerson struct{ Label string }

	// ReviewNext is a test helper for template semantics verification.
	ReviewNext struct{ Other string }

	// ReviewFirst is a test helper for template semantics verification.
	ReviewFirst struct {
		Label string
		Next  *ReviewNext
	}

	// ReviewKey is a defined string type to test map key assignability.
	ReviewKey string

	// ReviewMaker is a test helper for method-returning-callable verification.
	ReviewMaker struct{}
)

// PointerLabel is a pointer receiver method on ReviewPerson.
func (*ReviewPerson) PointerLabel() string { return "ptr" }

// Get returns a function returning ReviewPerson.
func (ReviewMaker) Get(string) func() ReviewPerson {
	return func() ReviewPerson { return ReviewPerson{Label: "ok"} }
}

func TestSemanticsReview(t *testing.T) {
	t.Parallel()

	funcs := template.FuncMap{
		"person":   func() ReviewPerson { return ReviewPerson{Label: "ok"} },
		"items":    func() []ReviewPerson { return []ReviewPerson{} },
		"nonempty": func() []ReviewPerson { return []ReviewPerson{{Label: "ok"}} },
		"matrix":   func() [][]ReviewPerson { return [][]ReviewPerson{{{Label: "ok"}}} },
		"empty":    func() string { return "" },
		"callable": func() func() ReviewPerson { return func() ReviewPerson { return ReviewPerson{Label: "ok"} } },
		"first":    func() ReviewFirst { return ReviewFirst{Label: "ok", Next: &ReviewNext{Other: "x"}} },
		"namedmap": func() map[ReviewKey]ReviewPerson { return map[ReviewKey]ReviewPerson{"Entry": {Label: "ok"}} },
		"maker":    func() ReviewMaker { return ReviewMaker{} },
	}
	cases := []struct {
		name string
		src  string
	}{
		{"if_else_contamination", `{{$x := person}}{{if false}}{{$x = "s"}}{{else}}{{$x.Label}}{{end}}`},
		{"outer_assignment", `{{$x := "s"}}{{if true}}{{$x = person}}{{end}}{{$x.Label}}`},
		{"if_pipeline_assignment", `{{$x := "s"}}{{if $x = person}}{{end}}{{$x.Label}}`},
		{"with_else_declaration", `{{with $x := empty}}{{else}}{{$x.Nope}}{{end}}`},
		{"range_else_declaration", `{{range $x := items}}{{else}}{{$x.Nope}}{{end}}`},
		{"and_known_result", `{{(and person person).Nope}}`},
		{"or_known_result", `{{(or person person).Nope}}`},
		{"slice_known_result", `{{(slice nonempty 0 1).Nope}}`},
		{"call_known_result", `{{(call callable).Nope}}`},
		{"index_pipeline_order", `{{with 0 | index matrix 0}}{{.Label}}{{end}}`},
		{"index_pipeline_known_result", `{{with 0 | index matrix 0}}{{range .}}{{.Label}}{{end}}{{end}}`},
		{"index_string_known_result", `{{(index "abc" 0).Nope}}`},
		{"pointer_method_unaddressable", `{{(person).PointerLabel}}`},
		{"named_string_map_key", `{{(namedmap).Entry.Label}}`},
		{"named_template_no_data", `{{define "show"}}{{.Label}}{{end}}{{with person}}{{template "show"}}{{end}}`},
		{"recursive_different_type", `{{define "recur"}}{{.Label}}{{with .Next}}{{template "recur" .}}{{end}}{{end}}{{template "recur" first}}`},
		{"method_returns_callable", `{{with (maker).Get "x"}}{{.Label}}{{end}}`},
		{"range_integer", `{{range 1}}{{.Nope}}{{end}}`},
		{"and_short_circuit_mutation", `{{$x := person}}{{and ($x = "text") ($x.Label)}}`},
	}
	spec := PromptSpec{
		ID:       "review",
		Role:     PromptRoleSystem,
		ArgsType: reflect.TypeFor[struct{ Seed string }](),
		Funcs:    funcs,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validation := ValidatePromptTemplate(spec, tc.src)
			compiled, err := template.New("review").Option("missingkey=error").Funcs(funcs).Parse(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			execution := compiled.Execute(&b, struct{ Seed string }{})
			t.Logf("source=%q validation=%v execution=%v output=%q", tc.src, validation, execution, b.String())
			if (validation == nil) != (execution == nil) {
				t.Fatalf("validation/execution mismatch: validation=%v execution=%v output=%q", validation, execution, b.String())
			}
			if execution != nil && !errors.Is(validation, ErrTemplateFieldNotFound) {
				t.Fatalf("expected ErrTemplateFieldNotFound when execution fails, got: %v", validation)
			}
		})
	}
}

type (
	// qa79Person is a test helper for semantic oracle review.
	qa79Person struct{ Label string }

	// qa79Slice is a test helper for slice addressability checks.
	qa79Slice []qa79Person

	// qa79Array is a test helper for array slicing checks.
	qa79Array [1]qa79Person

	// qa79Root is the root data structure for the semantic oracle suite.
	qa79Root struct {
		P     qa79Person
		Flag  bool
		Items []qa79Person
		Sl    qa79Slice
		Arr   qa79Array
		Seq   iter.Seq[qa79Person]
		Ch    chan string
	}
)

// PointerLabel is a pointer receiver method on qa79Slice.
func (*qa79Slice) PointerLabel() string { return "ptr-slice" }

// Label is a value receiver method on qa79Array.
func (qa79Array) Label() string { return "array-label" }

func TestQA79SemanticOracle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		src            string
		funcs          template.FuncMap
		wantValidation bool
		wantExecution  bool
	}{
		{"template_pipeline_assignment", `{{define "sub"}}{{end}}{{$x := .P}}{{template "sub" $x = "text"}}{{$x.Label}}`, nil, false, false},
		{"template_pipeline_declaration", `{{define "sub"}}{{end}}{{template "sub" $x := .P}}{{$x.Missing}}`, nil, false, false},
		{"nested_pipeline_assignment", `{{$x := .P}}{{printf "%s" ($x = "text")}}{{$x.Label}}`, nil, false, false},
		{"if_shadow_pipeline", `{{$x := .P}}{{if $x := true}}{{end}}{{$x.Label}}`, nil, true, true},
		{"if_shadow_body", `{{$x := .P}}{{if true}}{{$x := "inner"}}{{end}}{{$x.Label}}`, nil, true, true},
		{"with_shadow_pipeline", `{{$x := .P}}{{with $x := "inner"}}{{else}}{{end}}{{$x.Label}}`, nil, true, true},
		{"range_shadow_pipeline", `{{$x := .P}}{{range $x := .Items}}{{else}}{{end}}{{$x.Label}}`, nil, true, true},
		{"branch_unsafe_union", `{{$x := .P}}{{if .Flag}}{{$x = "text"}}{{end}}{{$x.Label}}`, nil, false, false},
		{"and_mixed_known_result", `{{(and .P "text").Missing}}`, nil, false, false},
		{"or_mixed_known_result", `{{(or .P "text").Missing}}`, nil, false, false},
		{"range_loop_reassignment", `{{$x := .P}}{{range .Items}}{{$x.Label}}{{$x = "text"}}{{end}}`, nil, false, false},
		{"slice_result_addressability", `{{(slice .Sl 0 1).PointerLabel}}`, nil, false, false},
		{"array_slice_result", `{{(slice .Arr 0 1).Label}}`, nil, false, false},
		{"iterator_range", `{{range .Seq}}{{.Label}}{{end}}`, nil, true, true},
		{"channel_index_type", `{{range $i,$x := .Ch}}{{$i.Missing}}{{end}}`, nil, false, false},
		{"template_without_data_range", `{{define "sub"}}{{range .}}body{{else}}empty{{end}}{{end}}{{template "sub"}}`, nil, true, true},
		{"builtin_override_printf", `{{(printf).Label}}`, template.FuncMap{"printf": func() qa79Person { return qa79Person{"override"} }}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan string, 1)
			ch <- "x"
			close(ch)
			data := &qa79Root{
				P:     qa79Person{"person"},
				Flag:  true,
				Items: []qa79Person{{"one"}, {"two"}},
				Arr:   qa79Array{{"arr"}},
				Sl:    qa79Slice{{"slice"}},
				Seq:   func(yield func(qa79Person) bool) { yield(qa79Person{"seq"}) },
				Ch:    ch,
			}
			spec := PromptSpec{
				ID:       "qa79",
				Role:     PromptRoleSystem,
				Template: tc.src,
				ArgsType: reflect.TypeFor[*qa79Root](),
				Funcs:    tc.funcs,
			}
			validation := ValidatePromptTemplate(spec, tc.src)
			raw, err := template.New("qa79").Option("missingkey=error").Funcs(tc.funcs).Parse(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			execution := raw.Execute(&b, data)
			reg := NewRegistry(nil)
			registration := reg.Register(spec)
			var renderErr error
			var renderText string
			if registration == nil {
				renderData := *data
				freshCh := make(chan string, 1)
				freshCh <- "x"
				close(freshCh)
				renderData.Ch = freshCh
				content, err := reg.Render(context.Background(), spec.ID, Scope{}, &renderData)
				renderErr = err
				if content != nil {
					renderText = content.Text
				}
			}
			t.Logf("source=%q validation=%v execution=%v output=%q registration=%v render=%v rendered=%q",
				tc.src, validation, execution, b.String(), registration, renderErr, renderText)
			if (validation == nil) != tc.wantValidation || (execution == nil) != tc.wantExecution {
				t.Errorf("semantic oracle expected validationOK=%t executionOK=%t; got validationOK=%t executionOK=%t",
					tc.wantValidation, tc.wantExecution, validation == nil, execution == nil)
			}
		})
	}
}

func TestQA79UnusedDefinition(t *testing.T) {
	t.Parallel()

	type rootData struct {
		P qa79Person
	}

	const source = `{{define "unused"}}{{.Other}}{{end}}Hello {{.P.Label}}`
	spec := PromptSpec{
		ID:       "qa79.unused",
		Role:     PromptRoleSystem,
		Template: source,
		ArgsType: reflect.TypeFor[rootData](),
	}

	// 1. ValidatePromptTemplate should succeed because "unused" is never executed.
	if err := ValidatePromptTemplate(spec, source); err != nil {
		t.Fatalf("ValidatePromptTemplate: expected nil, got %v", err)
	}

	// 2. Register should succeed.
	reg := NewRegistry(NewInMemoryStore())
	if err := reg.Register(spec); err != nil {
		t.Fatalf("Register: expected nil, got %v", err)
	}

	// 3. store.Set override + Render should succeed.
	data := rootData{P: qa79Person{Label: "world"}}
	res, err := reg.Render(context.Background(), spec.ID, Scope{}, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.Text != "Hello world" {
		t.Fatalf("Render: expected 'Hello world', got %q", res.Text)
	}
}
