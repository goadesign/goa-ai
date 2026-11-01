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

	"github.com/stretchr/testify/require"
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
		Template: "Hello {{.P.Label}}",
		ArgsType: reflect.TypeFor[rootData](),
	}

	// 1. ValidatePromptTemplate should succeed because "unused" is never executed.
	if err := ValidatePromptTemplate(spec, source); err != nil {
		t.Fatalf("ValidatePromptTemplate: expected nil, got %v", err)
	}

	// 2. Register should succeed.
	store := NewInMemoryStore()
	reg := NewRegistry(store)
	if err := reg.Register(spec); err != nil {
		t.Fatalf("Register: expected nil, got %v", err)
	}

	// 3. store.Set override + Render should succeed.
	require.NoError(t, store.Set(context.Background(), spec.ID, Scope{}, source, nil))
	data := rootData{P: qa79Person{Label: "world"}}
	res, err := reg.Render(context.Background(), spec.ID, Scope{}, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.Text != "Hello world" {
		t.Fatalf("Render: expected 'Hello world', got %q", res.Text)
	}
}

type (
	// qa6283Person is a test helper for oracle testing.
	qa6283Person struct{ Label string }

	// qa6283Slice is a test helper for slice method lookup.
	qa6283Slice []qa6283Person

	// qa6283Embedded is an embedded struct pointer test helper.
	qa6283Embedded struct{ Child qa6283Person }

	// qa6283Root is the root struct for oracle testing.
	qa6283Root struct {
		*qa6283Embedded
		P       qa6283Person
		Items   []qa6283Person
		Flag    bool
		Count   int
		Sl      qa6283Slice
		Seq2    iter.Seq2[qa6283Person, string]
		Seq2Bad iter.Seq2[string, qa6283Person]
	}
)

// PointerLabel is a pointer receiver method on qa6283Person.
func (*qa6283Person) PointerLabel() string { return "pointer" }

// Label is a value receiver method on qa6283Slice.
func (qa6283Slice) Label() string { return "slice" }

// TestQA6283Oracle compares static acceptance at all registry entries with the
// module-pinned Go template executor. Its custom functions only run at execution.
func TestQA6283Oracle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		source         string
		funcs          template.FuncMap
		wantValidation bool
		wantExecution  bool
	}{
		{"third_iteration_reassignment", `{{$x := .P}}{{$y := .P}}{{$z := "text"}}{{range .Items}}{{$x.Label}}{{$x = $y}}{{$y = $z}}{{end}}`, nil, false, false},
		{"and_skips_assignment", `{{$x := .P}}{{and false ($x = "text")}}{{$x.Label}}`, nil, true, true},
		{"or_skips_assignment", `{{$x := .P}}{{or true ($x = "text")}}{{$x.Label}}`, nil, true, true},
		{"and_dynamic_skip_safe", `{{$x := .P}}{{and .Flag ($x = .P)}}{{$x.Label}}`, nil, true, true},
		{"and_struct_truth", `{{(and .P .P).Label}}`, nil, true, true},
		{"or_struct_truth", `{{(or .P "text").Label}}`, nil, true, true},
		{"break_skips_assignment", `{{$x := .P}}{{range .Items}}{{break}}{{$x = "text"}}{{end}}`, nil, true, true},
		{"continue_skips_assignment", `{{$x := .P}}{{range .Items}}{{continue}}{{$x = "text"}}{{end}}`, nil, true, true},
		{"break_stops_loop", `{{$x := .P}}{{range .Items}}{{$x.Label}}{{$x = "text"}}{{break}}{{end}}`, nil, true, true},
		{"constant_false_dead_assignment", `{{$x := .P}}{{if false}}{{$x = "text"}}{{end}}{{$x.Label}}`, nil, true, true},
		{"constant_true_dead_else_assignment", `{{$x := .P}}{{if true}}{{else}}{{$x = "text"}}{{end}}{{$x.Label}}`, nil, true, true},
		{"embedded_pointer_addressable", `{{.Child.PointerLabel}}`, nil, true, true},
		{"custom_render_struct_return", `{{(render).Label}}`, template.FuncMap{"render": func() qa6283Person { return qa6283Person{"custom"} }}, true, true},
		{"range_integer_two_vars", `{{range $i,$x := .Count}}{{$x}}{{end}}`, nil, false, false},
		{"nested_index_double_assignment", `{{$x := .P}}{{$y := .P}}{{index ($x = (printf "%v" ($y = "text"))) 0}}{{$y.Label}}`, nil, false, false},
		{"range_single_array_break", `{{$x := .P}}{{range .Items}}{{$x.Label}}{{break}}{{$x = "text"}}{{end}}`, nil, true, true},
		{"index_reexec_restores_earlier_assignment", `{{$x := .Sl}}{{index ($x = .Sl) (len ($x = "text"))}}{{$x.Label}}`, nil, false, false},
		{"slice_reexec_restores_earlier_assignment", `{{$x := .Sl}}{{slice ($x = .Sl) 0 (len ($x = "text"))}}{{$x.Label}}`, nil, false, false},
		{"custom_reflect_value", `{{(value).Label}}`, template.FuncMap{"value": func() reflect.Value { return reflect.ValueOf(qa6283Person{"value"}) }}, true, true},
		{"range_zero_array_skips_assignment", `{{$x := .P}}{{range .Zero}}{{$x = "text"}}{{end}}{{$x.Label}}`, nil, true, true},
		{"seq2_one_variable_yields_key", `{{range $x := .Seq2}}{{$x.Label}}{{end}}`, nil, true, true},
		{"seq2_no_variable_yields_key", `{{range .Seq2}}{{.Label}}{{end}}`, nil, true, true},
		{"seq2_invalid_one_variable_member", `{{range $x := .Seq2Bad}}{{$x.Label}}{{end}}`, nil, false, false},
		{"seq2_invalid_no_variable_member", `{{range .Seq2Bad}}{{.Label}}{{end}}`, nil, false, false},
		{"seq2_two_variables_yields_value", `{{range $key,$value := .Seq2Bad}}{{$value.Label}}{{end}}`, nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := struct {
				qa6283Root
				Zero [0]int
			}{
				qa6283Root: qa6283Root{
					qa6283Embedded: &qa6283Embedded{Child: qa6283Person{"child"}},
					P:              qa6283Person{"person"},
					Items:          []qa6283Person{{"one"}, {"two"}, {"three"}},
					Count:          1,
					Flag:           false,
					Sl:             qa6283Slice{{"zero"}, {"one"}, {"two"}, {"three"}, {"four"}},
				},
			}
			data.Seq2 = func(yield func(qa6283Person, string) bool) { yield(qa6283Person{"key"}, "value") }
			data.Seq2Bad = func(yield func(string, qa6283Person) bool) { yield("key", qa6283Person{"value"}) }
			spec := PromptSpec{
				ID:       "qa6283",
				Role:     PromptRoleSystem,
				Template: tc.source,
				ArgsType: reflect.TypeOf(data),
				Funcs:    tc.funcs,
			}
			validation := ValidatePromptTemplate(spec, tc.source)
			registration := NewRegistry(nil).Register(spec)
			store := NewInMemoryStore()
			reg := NewRegistry(store)
			baseline := spec
			baseline.Template = `{{.P.Label}}`
			if err := reg.Register(baseline); err != nil {
				t.Fatal(err)
			}
			override := reg.ValidateOverride(spec.ID, tc.source)
			if err := store.Set(context.Background(), spec.ID, Scope{}, tc.source, nil); err != nil {
				t.Fatal(err)
			}
			content, renderErr := reg.Render(context.Background(), spec.ID, Scope{}, data)
			parsed, err := template.New(spec.ID.String()).Option("missingkey=error").Funcs(tc.funcs).Parse(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			execution := parsed.Execute(&output, data)
			t.Logf("source=%q validation=%v registration=%v override=%v raw_render=%v content=%v stdlib_execution=%v output=%q",
				tc.source, validation, registration, override, renderErr, content, execution, output.String())
			if (execution == nil) != tc.wantExecution {
				t.Errorf("stdlib executionOK=%v want=%v", execution == nil, tc.wantExecution)
			}
			for name, got := range map[string]error{"validation": validation, "registration": registration, "override": override} {
				if (got == nil) != tc.wantValidation {
					t.Errorf("%s accepted=%v want=%v", name, got == nil, tc.wantValidation)
				}
			}
			if tc.wantValidation && renderErr != nil {
				t.Errorf("valid raw override rejected: %v", renderErr)
			}
		})
	}
}

type (
	// qa254Good is a test helper for adversarial oracle testing.
	qa254Good struct{ Label string }

	// qa254Data is a root struct for adversarial oracle testing.
	qa254Data struct {
		Good     qa254Good
		Bad      string
		Rows     []int
		One      [1]int
		Zero     [0]int
		Flag     bool
		Children []qa254Good
		Seq1     iter.Seq[qa254Good]
		Seq2     iter.Seq2[qa254Good, qa254Good]
		SeqAny   iter.Seq[any]
		Value    reflect.Value
		PValue   *reflect.Value
	}

	// qa254RecursivePointer tests termination on recursive pointer types.
	qa254RecursivePointer *qa254RecursivePointer
)

// TestQA254RecursivePointer checks that static typing terminates on valid recursive pointer types.
func TestQA254RecursivePointer(t *testing.T) {
	t.Parallel()

	var data qa254RecursivePointer
	source := "literal"
	parsed, err := template.New("oracle").Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, data); err != nil {
		t.Fatal(err)
	}
	t.Logf("oracle=%q; invoking ValidatePromptTemplate with type=%s", output.String(), reflect.TypeOf(data))
	spec := PromptSpec{ID: "recursive", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(data)}
	if err := ValidatePromptTemplate(spec, source); err != nil {
		t.Fatal(err)
	}
}

// TestQA254Adversarial compares every prompt entry with actual template execution.
func TestQA254Adversarial(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
		funcs  template.FuncMap
		wantOK bool
	}{
		{"custom_argument_missing", `{{consume .Missing}}`, template.FuncMap{"consume": func(any) string { return "ok" }}, false},
		{"render_argument_missing", `{{render "child" .Missing}}`, nil, false},
		{"custom_argument_assignment", `{{$x := .Good}}{{consume ($x = .Bad)}}{{$x.Label}}`, template.FuncMap{"consume": func(any) string { return "ok" }}, false},
		{"custom_nested_argument", `{{consume (printf "%s" .Missing)}}`, template.FuncMap{"consume": func(any) string { return "ok" }}, false},
		{"nested_break_tail", `{{$x := .Good}}{{range .Rows}}{{if true}}{{break}}{{end}}{{$x = $.Bad}}{{end}}{{$x.Label}}`, nil, true},
		{"nested_continue_tail", `{{$x := .Good}}{{range .Rows}}{{with $.Good}}{{continue}}{{end}}{{$x = $.Bad}}{{end}}{{$x.Label}}`, nil, true},
		{"nested_break_stops_iterations", `{{$x := .Good}}{{range .Rows}}{{$x.Label}}{{$x = $.Bad}}{{if true}}{{break}}{{end}}{{end}}`, nil, true},
		{"unreachable_break_after_continue", `{{$x := .Good}}{{range .Rows}}{{$x.Label}}{{$x = $.Bad}}{{continue}}{{break}}{{end}}`, nil, false},
		{"unreachable_break_after_nested_continue", `{{$x := .Good}}{{range .Rows}}{{$x.Label}}{{$x = $.Bad}}{{if true}}{{continue}}{{end}}{{break}}{{end}}`, nil, false},
		{"literal_zero_iterations", `{{$x := .Good}}{{range 0}}{{$x = $.Bad}}{{end}}{{$x.Label}}`, nil, true},
		{"literal_one_iteration", `{{$x := .Good}}{{range 1}}{{$x.Label}}{{$x = $.Bad}}{{end}}`, nil, true},
		{"one_array_iteration", `{{$x := .Good}}{{range .One}}{{$x.Label}}{{$x = $.Bad}}{{end}}`, nil, true},
		{"constant_or_branch", `{{$x := .Good}}{{if or false true}}{{else}}{{$x = .Bad}}{{end}}{{$x.Label}}`, nil, true},
		{"constant_and_branch", `{{$x := .Good}}{{if and true true}}{{else}}{{$x = .Bad}}{{end}}{{$x.Label}}`, nil, true},
		{"reflect_field_member", `{{.Value.Missing}}`, nil, false},
		{"reflect_field_method_result", `{{.Value.String.Missing}}`, nil, false},
		{"reflect_pointer_field_member", `{{.PValue.Missing}}`, nil, false},
		{"seq2_slice_union_one_variable", `{{$xs := .Children}}{{if .Flag}}{{$xs = .Seq2}}{{end}}{{range $v := $xs}}{{$v.Label}}{{end}}`, nil, true},
		{"seq2_slice_union_no_variable", `{{$xs := .Children}}{{if .Flag}}{{$xs = .Seq2}}{{end}}{{range $xs}}{{.Label}}{{end}}`, nil, true},
		{"seq2_seq1_union_invalid_member", `{{$xs := .Seq1}}{{if .Flag}}{{$xs = .Seq2}}{{end}}{{range $v := $xs}}{{$v.Missing}}{{end}}`, nil, false},
		{"seq2_seq1_union_valid_member", `{{$xs := .Seq1}}{{if .Flag}}{{$xs = .Seq2}}{{end}}{{range $v := $xs}}{{$v.Label}}{{end}}`, nil, true},
		{"seq1_dynamic_two_variables", `{{range $k,$v := .SeqAny}}{{$v}}{{end}}`, nil, false},
		{"seq2_two_variables_correct", `{{range $k,$v := .Seq2}}{{$k.Label}}:{{$v.Label}}{{end}}`, nil, true},
		{"body_declaration_converges", `{{$x := .Good}}{{range .Rows}}{{$local := .}}{{$x = $.Good}}{{end}}{{$x.Label}}`, nil, true},
		{"swap_declaration_converges", `{{$x := .Good}}{{$y := .Good}}{{range .Rows}}{{$tmp := $x}}{{$x = $y}}{{$y = $tmp}}{{end}}{{$x.Label}}`, nil, true},
		{"and_nested_shortcircuit_safe", `{{$x := .Good}}{{and .Flag false ($x = .Bad)}}{{$x.Label}}`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, flag := range []bool{false, true} {
				rv := reflect.ValueOf(qa254Good{"wrapped"})
				data := qa254Data{Good: qa254Good{"good"}, Bad: "bad", Rows: []int{0, 1, 2}, One: [1]int{0}, Flag: flag, Children: []qa254Good{{"child"}}, Value: rv, PValue: &rv}
				data.Seq1 = func(yield func(qa254Good) bool) { yield(qa254Good{"seq1"}) }
				data.Seq2 = func(yield func(qa254Good, qa254Good) bool) { yield(qa254Good{"key"}, qa254Good{"value"}) }
				data.SeqAny = func(yield func(any) bool) { yield(qa254Good{"any"}) }
				calls := 0
				funcs := template.FuncMap{"emit": func() string { calls++; return "" }}
				for k, v := range tc.funcs {
					funcs[k] = v
				}
				source := `{{emit}}` + tc.source
				spec := PromptSpec{ID: "qa254", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(data), Funcs: funcs}
				validation := ValidatePromptTemplate(spec, source)
				registration := NewRegistry(nil).Register(spec)
				store := NewInMemoryStore()
				reg := NewRegistry(store)
				base := spec
				base.Template = "baseline"
				if err := reg.Register(base); err != nil {
					t.Fatal(err)
				}
				override := reg.ValidateOverride(spec.ID, source)
				validationCalls := calls
				if err := store.Set(context.Background(), spec.ID, Scope{}, source, nil); err != nil {
					t.Fatal(err)
				}
				content, renderErr := reg.Render(context.Background(), spec.ID, Scope{}, data)
				renderCalls := calls
				oracleFuncs := template.FuncMap{"render": func(string, ...any) (string, error) { return "", nil }}
				for k, v := range funcs {
					oracleFuncs[k] = v
				}
				parsed, err := template.New("oracle").Option("missingkey=error").Funcs(oracleFuncs).Parse(source)
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				execution := parsed.Execute(&output, data)
				t.Logf("flag=%t source=%q validate=%v register=%v override=%v raw_render=%v validation_calls=%d render_calls=%d content=%v oracle=%v output=%q", flag, tc.source, validation, registration, override, renderErr, validationCalls, renderCalls, content, execution, output.String())
				if validationCalls != 0 {
					t.Errorf("validation executed custom function")
				}
				if (execution == nil) != tc.wantOK {
					t.Errorf("oracle OK=%t want=%t", execution == nil, tc.wantOK)
				}
				for name, got := range map[string]error{"validate": validation, "register": registration, "override": override} {
					if (got == nil) != tc.wantOK {
						t.Errorf("%s OK=%t want=%t", name, got == nil, tc.wantOK)
					}
				}
				if tc.wantOK && renderErr != nil {
					t.Errorf("valid raw override rejected: %v", renderErr)
				}
				if !tc.wantOK && renderCalls != 0 {
					t.Errorf("invalid raw override reached Execute")
				}
			}
		})
	}
}

type (
	// qa293Good supplies the only element-level field used by these oracle tests.
	qa293Good struct{ Label string }

	// qa293FlowData separates outer fields from element fields and provides known range lengths.
	qa293FlowData struct {
		Good     qa293Good
		Bad      string
		Root     string
		Flag     bool
		Children []qa293Good
		One      [1]qa293Good
		Empty    [0]qa293Good
		Numbers  []int
	}

	// qa293Item is a test helper for reflection method and addressability verification.
	qa293Item struct{ Label string }

	// qa293Embedded is an embedded struct test helper.
	qa293Embedded struct{ Item qa293Item }

	// qa293Promoted tests promoted pointer addressability.
	qa293Promoted struct{ *qa293Embedded }

	// qa293Bool is a defined boolean type to verify iterator return type exactness.
	qa293Bool bool

	// qa293Self tests termination on self-referential recursive pointers.
	qa293Self *qa293Self

	// qa293MutualA tests mutual recursive pointer cycles.
	qa293MutualA *qa293MutualB

	// qa293MutualB tests mutual recursive pointer cycles.
	qa293MutualB *qa293MutualA
)

// Pointer returns "ptr" to test pointer receiver method resolution.
func (*qa293Item) Pointer() string { return "ptr" }

// Wrapped returns reflect.Value of qa293Item to test value receiver method resolution.
func (qa293Item) Wrapped() reflect.Value { return reflect.ValueOf(qa293Item{"wrapped"}) }

// TestQA293FreshFlow compares the same source at each prompt boundary and in Go's template executor.
func TestQA293FreshFlow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		source   string
		staticOK bool
		oracleOK [2]bool
	}{
		{"range_decl_dot_valid", `{{range $v := .Children}}{{.Label}}{{end}}`, true, [2]bool{true, true}},
		{"range_decl_dot_invalid", `{{range $v := .Children}}{{.Root}}{{end}}`, false, [2]bool{false, false}},
		{"range_assign_dot_valid", `{{$v := .Good}}{{range $v = .Children}}{{.Label}}{{end}}`, true, [2]bool{true, true}},
		{"range_two_decls_dot_valid", `{{range $i,$v := .Children}}{{.Label}}{{end}}`, true, [2]bool{true, true}},
		{"pipeline_or_invalid", `{{(.Good | or .Bad).Label}}`, false, [2]bool{false, false}},
		{"pipeline_or_valid", `{{(.Bad | or .Good).Label}}`, true, [2]bool{true, true}},
		{"pipeline_and_invalid", `{{(.Bad | and .Good).Label}}`, false, [2]bool{false, false}},
		{"pipeline_and_unknown_first_invalid", `{{(.Good | and .Bad).Label}}`, false, [2]bool{true, true}},
		{"pipeline_and_valid", `{{(.Good | and .One).Label}}`, true, [2]bool{true, true}},
		{"or_unknown_single", `{{$x := .Good}}{{if or .Flag}}{{$x = .Bad}}{{end}}{{$x.Label}}`, false, [2]bool{true, false}},
		{"and_unknown_single", `{{$x := .Good}}{{if and .Flag}}{{else}}{{$x = .Bad}}{{end}}{{$x.Label}}`, false, [2]bool{false, true}},
		{"or_unknown_last", `{{$x := .Good}}{{if or false .Flag}}{{$x = .Bad}}{{end}}{{$x.Label}}`, false, [2]bool{true, false}},
		{"and_unknown_last", `{{$x := .Good}}{{if and true .Flag}}{{else}}{{$x = .Bad}}{{end}}{{$x.Label}}`, false, [2]bool{false, true}},
		{"merge_opposite_truth", `{{$x := .Good}}{{$ok := true}}{{if .Flag}}{{$ok = false}}{{end}}{{if $ok}}{{$x = .Bad}}{{end}}{{$x.Label}}`, false, [2]bool{false, true}},
		{"merge_opposite_truth_else", `{{$x := .Good}}{{$ok := false}}{{if .Flag}}{{$ok = true}}{{end}}{{if $ok}}{{else}}{{$x = .Bad}}{{end}}{{$x.Label}}`, false, [2]bool{false, true}},
		{"loop_truth_change", `{{$x := .Good}}{{$ok := true}}{{range .Numbers}}{{if $ok}}{{$ok = false}}{{else}}{{$x = $.Bad}}{{end}}{{end}}{{$x.Label}}`, false, [2]bool{false, false}},
		{"named_call_truth_change", `{{define "sub"}}{{$x := good}}{{if .}}{{else}}{{$x = bad}}{{end}}{{$x.Label}}{{end}}{{template "sub" true}}{{template "sub" false}}`, false, [2]bool{false, false}},
		{"range_zero_assignment", `{{$x := .Good}}{{range $x = .Empty}}{{end}}{{$x.Label}}`, false, [2]bool{false, false}},
		{"range_zero_nested_assignment", `{{$x := .Good}}{{range ($x = .Empty)}}{{end}}{{$x.Label}}`, false, [2]bool{false, false}},
		{"custom_func_argument_valid", `{{consume .Good.Label}}`, true, [2]bool{true, true}},
		{"custom_func_argument_invalid", `{{consume .Missing}}`, false, [2]bool{false, false}},
		{"new_pipeline_or_assignment", `{{$x := .Good}}{{true | or ($x = .Bad)}}{{$x.Label}}`, false, [2]bool{false, false}},
		{"new_pipeline_and_assignment", `{{$x := .Good}}{{false | and ($x = .Bad)}}{{$x.Label}}`, false, [2]bool{false, false}},
		{"new_single_range_break_valid", `{{$x := .Good}}{{range 1}}{{if true}}{{break}}{{end}}{{$x = $.Bad}}{{end}}{{$x.Label}}`, true, [2]bool{true, true}},
		{"new_single_range_continue_valid", `{{$x := .Good}}{{range 1}}{{with $.Good}}{{continue}}{{end}}{{$x = $.Bad}}{{end}}{{$x.Label}}`, true, [2]bool{true, true}},
		{"new_zero_range_body_valid", `{{$x := .Good}}{{range 0}}{{$x = $.Bad}}{{end}}{{$x.Label}}`, true, [2]bool{true, true}},
		{"new_shadow_in_break_valid", `{{$x := .Good}}{{range 1}}{{if true}}{{$x := $.Bad}}{{break}}{{end}}{{end}}{{$x.Label}}`, true, [2]bool{true, true}},
		{"new_shadow_in_continue_valid", `{{$x := .Good}}{{range 1}}{{if true}}{{$x := $.Bad}}{{continue}}{{end}}{{end}}{{$x.Label}}`, true, [2]bool{true, true}},
		{"new_unknown_branch_both_break_valid", `{{$x := .Good}}{{range .Numbers}}{{if $.Flag}}{{break}}{{else}}{{break}}{{end}}{{$x = $.Bad}}{{end}}{{$x.Label}}`, true, [2]bool{true, true}},
		{"new_inner_break_outer_continues", `{{$x := .Good}}{{range .Numbers}}{{$x.Label}}{{range 1}}{{break}}{{end}}{{$x = $.Bad}}{{end}}`, false, [2]bool{false, false}},
		{"new_single_continue_post_invalid", `{{$x := .Good}}{{range 1}}{{$x = $.Bad}}{{continue}}{{end}}{{$x.Label}}`, false, [2]bool{false, false}},
		{"new_branch_assignment_persists", `{{$x := .Good}}{{if $x = .Bad}}{{end}}{{$x.Label}}`, false, [2]bool{false, false}},
		{"new_known_two_exact_valid", `{{$x := .Good}}{{$y := .Good}}{{range 2}}{{$x.Label}}{{$x = $y}}{{$y = $.Bad}}{{end}}`, true, [2]bool{true, true}},
		{"new_piped_index_invalid", `{{(.Good | index).Missing}}`, false, [2]bool{false, false}},
		{"new_dead_decl_invalid", `{{range 1}}{{break}}{{$x := $.Good}}{{$x.Missing}}{{end}}`, false, [2]bool{true, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i, flag := range []bool{false, true} {
				data := qa293FlowData{
					Good:     qa293Good{"good"},
					Bad:      "bad",
					Root:     "outer",
					Flag:     flag,
					Children: []qa293Good{{"child"}},
					One:      [1]qa293Good{{"one"}},
					Numbers:  []int{0, 1},
				}
				calls := 0
				funcs := template.FuncMap{
					"emit":    func() string { calls++; return "" },
					"good":    func() qa293Good { return qa293Good{"good"} },
					"bad":     func() string { return "bad" },
					"consume": func(any) string { return "ok" },
				}
				source := `{{emit}}` + tc.source
				spec := PromptSpec{
					ID:       "qa293",
					Role:     PromptRoleSystem,
					Template: source,
					ArgsType: reflect.TypeOf(data),
					Funcs:    funcs,
				}
				validation := ValidatePromptTemplate(spec, source)
				registration := NewRegistry(nil).Register(spec)
				store := NewInMemoryStore()
				reg := NewRegistry(store)
				base := spec
				base.Template = "baseline"
				if err := reg.Register(base); err != nil {
					t.Fatal(err)
				}
				override := reg.ValidateOverride(spec.ID, source)
				validationCalls := calls
				if err := store.Set(context.Background(), spec.ID, Scope{}, source, nil); err != nil {
					t.Fatal(err)
				}
				content, renderErr := reg.Render(context.Background(), spec.ID, Scope{}, data)
				renderCalls := calls
				parsed, err := template.New("oracle").Option("missingkey=error").Funcs(funcs).Parse(source)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				oracleErr := parsed.Execute(&out, data)
				t.Logf("flag=%t source=%q validate=%v register=%v override=%v raw_render=%v validation_calls=%d render_calls=%d content=%v oracle=%v output=%q", flag, tc.source, validation, registration, override, renderErr, validationCalls, renderCalls, content, oracleErr, out.String())
				if validationCalls != 0 {
					t.Errorf("validation ran helpers: %d", validationCalls)
				}
				if (oracleErr == nil) != tc.oracleOK[i] {
					t.Errorf("oracle OK=%t want=%t", oracleErr == nil, tc.oracleOK[i])
				}
				for name, got := range map[string]error{"validate": validation, "register": registration, "override": override} {
					if (got == nil) != tc.staticOK {
						t.Errorf("%s OK=%t want=%t", name, got == nil, tc.staticOK)
					}
					if !tc.staticOK && got != nil && !errors.Is(got, ErrTemplateFieldNotFound) {
						t.Errorf("%s wrong error: %v", name, got)
					}
				}
				if !tc.staticOK {
					if !errors.Is(renderErr, ErrTemplateFieldNotFound) {
						t.Errorf("raw render should reject statically: %v", renderErr)
					}
					if renderCalls != 0 {
						t.Errorf("invalid template ran helper: %d", renderCalls)
					}
				} else if renderErr != nil {
					t.Errorf("valid raw render: %v", renderErr)
				}
			}
		})
	}
}

// TestQA293CustomAndDynamic preserves custom builtin names and manual dynamic composition.
func TestQA293CustomAndDynamic(t *testing.T) {
	t.Parallel()

	data := qa293FlowData{Good: qa293Good{"good"}, Bad: "bad", Flag: true}
	cases := []struct {
		name, source string
		funcs        template.FuncMap
		valid        bool
	}{
		{"custom_or", `{{(or .Flag .Good).Label}}`, template.FuncMap{"or": func(any, any) qa293Good { return qa293Good{"custom"} }}, true},
		{"custom_or_invalid_arg", `{{(or .Missing .Good).Label}}`, template.FuncMap{"or": func(any, any) qa293Good { return qa293Good{"custom"} }}, false},
		{"custom_and", `{{(and .Flag .Good).Label}}`, template.FuncMap{"and": func(any, any) qa293Good { return qa293Good{"custom"} }}, true},
		{"custom_not", `{{(not .Flag).Label}}`, template.FuncMap{"not": func(any) qa293Good { return qa293Good{"custom"} }}, true},
		{"custom_index", `{{(index .Flag).Label}}`, template.FuncMap{"index": func(any) qa293Good { return qa293Good{"custom"} }}, true},
		{"custom_render", `{{(render "child" .Good).Label}}`, template.FuncMap{"render": func(string, any) qa293Good { return qa293Good{"custom"} }}, true},
		{"custom_arg_order_valid", `{{$x := .Bad}}{{consume ($x = .Good) $x.Label}}`, template.FuncMap{"consume": func(...any) string { return "ok" }}, true},
		{"custom_arg_order_invalid", `{{$x := .Good}}{{consume ($x = .Bad) $x.Label}}`, template.FuncMap{"consume": func(...any) string { return "ok" }}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			funcs := template.FuncMap{"emit": func() string { calls++; return "" }}
			for name, fn := range tc.funcs {
				funcs[name] = fn
			}
			source := `{{emit}}` + tc.source
			spec := PromptSpec{ID: "custom", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(data), Funcs: funcs}
			validation := ValidatePromptTemplate(spec, source)
			registration := NewRegistry(nil).Register(spec)
			store := NewInMemoryStore()
			reg := NewRegistry(store)
			base := spec
			base.Template = "baseline"
			if err := reg.Register(base); err != nil {
				t.Fatal(err)
			}
			override := reg.ValidateOverride(spec.ID, source)
			if calls != 0 {
				t.Fatalf("validation invoked witness: %d", calls)
			}
			if err := store.Set(context.Background(), spec.ID, Scope{}, source, nil); err != nil {
				t.Fatal(err)
			}
			_, renderErr := reg.Render(context.Background(), spec.ID, Scope{}, data)
			renderCalls := calls
			parsed, err := template.New("oracle").Option("missingkey=error").Funcs(funcs).Parse(source)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			oracleErr := parsed.Execute(&out, data)
			t.Logf("source=%q validate=%v register=%v override=%v render=%v render_calls=%d oracle=%v output=%q", source, validation, registration, override, renderErr, renderCalls, oracleErr, out.String())
			for name, got := range map[string]error{"validate": validation, "register": registration, "override": override, "render": renderErr, "oracle": oracleErr} {
				if (got == nil) != tc.valid {
					t.Errorf("%s OK=%t want=%t: %v", name, got == nil, tc.valid, got)
				}
			}
			if !tc.valid && renderCalls != 0 {
				t.Errorf("invalid source executed witness")
			}
		})
	}
	t.Run("nil_args_dynamic_composition", func(t *testing.T) {
		reg := NewRegistry(nil)
		for _, spec := range []PromptSpec{
			{ID: "parent", Role: PromptRoleSystem, Template: `parent {{render "child" .}}`},
			{ID: "child", Role: PromptRoleSystem, Template: `{{.Label}}`},
		} {
			if err := reg.Register(spec); err != nil {
				t.Fatal(err)
			}
		}
		got, err := reg.Render(context.Background(), "parent", Scope{}, map[string]any{"Label": "dynamic"})
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != "parent dynamic" {
			t.Errorf("text=%q", got.Text)
		}
		t.Logf("text=%q refs=%v", got.Text, got.Refs)
	})
}

// TestQA293Reflection compares declared-type validation and all registry entries
// with actual Go template execution, without executing functions in validation.
func TestQA293Reflection(t *testing.T) {
	t.Parallel()

	rv := reflect.ValueOf(qa293Item{"root"})
	item := qa293Item{"item"}
	root := struct {
		V             reflect.Value
		PV            *reflect.Value
		Item          qa293Item
		Slice         []qa293Item
		Array         [1]qa293Item
		Map           map[string]qa293Item
		Embedded      qa293Promoted
		Seq           iter.Seq[qa293Item]
		Seq2          iter.Seq2[qa293Item, string]
		SeqAny        iter.Seq[any]
		NamedBool     func(func(qa293Item) qa293Bool)
		Variadic      func(...func(qa293Item) bool)
		VariadicYield func(func(...qa293Item) bool)
	}{V: rv, PV: &rv, Item: item, Slice: []qa293Item{item}, Array: [1]qa293Item{item}, Map: map[string]qa293Item{"one": item}, Embedded: qa293Promoted{&qa293Embedded{item}}}
	root.Seq = func(yield func(qa293Item) bool) { yield(item) }
	root.Seq2 = func(yield func(qa293Item, string) bool) { yield(item, "text") }
	root.SeqAny = func(yield func(any) bool) { yield(item) }
	root.NamedBool = func(yield func(qa293Item) qa293Bool) { yield(item) }
	root.Variadic = func(yield ...func(qa293Item) bool) { yield[0](item) }
	root.VariadicYield = func(yield func(...qa293Item) bool) { yield(item) }
	cases := []struct {
		name   string
		source string
		data   any
		funcs  template.FuncMap
		want   bool
	}{
		{"root_value", `{{.Label}}`, rv, nil, true},
		{"root_pointer_value_method", `{{.String}}`, &rv, nil, true},
		{"root_pointer_value_missing", `{{.Missing}}`, &rv, nil, false},
		{"value_field_method", `{{.V.String}}`, root, nil, true},
		{"value_field_missing", `{{.V.Missing}}`, root, nil, false},
		{"value_pointer_field_method", `{{.PV.String}}`, root, nil, true},
		{"value_pointer_field_missing", `{{.PV.Missing}}`, root, nil, false},
		{"method_value_return", `{{.Item.Wrapped.Label}}`, root, nil, true},
		{"func_value_return", `{{(wrapped).Label}}`, root, template.FuncMap{"wrapped": func() reflect.Value { return rv }}, true},
		{"call_value_return", `{{(call .).Label}}`, func() reflect.Value { return rv }, nil, false},
		{"call_value_string", `{{(call .).String}}`, func() reflect.Value { return rv }, nil, true},
		{"call_item_return", `{{(call .).Label}}`, func() qa293Item { return item }, nil, true},
		{"call_field_value_return", `{{(call .Fn).Label}}`, struct{ Fn func() reflect.Value }{func() reflect.Value { return rv }}, nil, false},
		{"call_field_value_string", `{{(call .Fn).String}}`, struct{ Fn func() reflect.Value }{func() reflect.Value { return rv }}, nil, true},
		{"custom_call_value_return", `{{(call).Label}}`, root, template.FuncMap{"call": func() reflect.Value { return rv }}, true},
		{"struct_unaddressable", `{{.Item.Pointer}}`, root, nil, false},
		{"struct_addressable", `{{.Item.Pointer}}`, &root, nil, true},
		{"slice_element_addressable", `{{(index .Slice 0).Pointer}}`, root, nil, true},
		{"array_element_unaddressable", `{{(index .Array 0).Pointer}}`, root, nil, false},
		{"array_element_addressable", `{{(index .Array 0).Pointer}}`, &root, nil, true},
		{"map_element_unaddressable", `{{(index .Map "one").Pointer}}`, root, nil, false},
		{"promoted_pointer_addressable", `{{.Embedded.Item.Pointer}}`, root, nil, true},
		{"seq_one", `{{range .Seq}}{{.Label}}{{end}}`, root, nil, true},
		{"seq_two_invalid", `{{range $k,$v := .Seq}}{{$v.Label}}{{end}}`, root, nil, false},
		{"seq2_one", `{{range $v := .Seq2}}{{$v.Label}}{{end}}`, root, nil, true},
		{"seq2_none", `{{range .Seq2}}{{.Label}}{{end}}`, root, nil, true},
		{"seq2_two", `{{range $k,$v := .Seq2}}{{$k.Label}}:{{$v}}{{end}}`, root, nil, true},
		{"seq_any_two_invalid", `{{range $k,$v := .SeqAny}}{{$v}}{{end}}`, root, nil, false},
		{"named_bool_iterator", `{{range .NamedBool}}{{.Label}}{{end}}`, root, nil, false},
		{"variadic_iterator", `{{range .Variadic}}{{.Label}}{{end}}`, root, nil, false},
		{"variadic_yield_iterator", `{{range .VariadicYield}}{{.}}{{end}}`, root, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			funcs := template.FuncMap{"emit": func() string { calls++; return "" }}
			for k, v := range tc.funcs {
				funcs[k] = v
			}
			source := `{{emit}}` + tc.source
			spec := PromptSpec{ID: "qa293", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(tc.data), Funcs: funcs}
			validation := ValidatePromptTemplate(spec, source)
			registration := NewRegistry(nil).Register(spec)
			store := NewInMemoryStore()
			reg := NewRegistry(store)
			baseline := spec
			baseline.Template = "baseline"
			if err := reg.Register(baseline); err != nil {
				t.Fatal(err)
			}
			override := reg.ValidateOverride(spec.ID, source)
			validationCalls := calls
			if err := store.Set(context.Background(), spec.ID, Scope{}, source, nil); err != nil {
				t.Fatal(err)
			}
			content, renderErr := reg.Render(context.Background(), spec.ID, Scope{}, tc.data)
			renderCalls := calls
			parsed, err := template.New("oracle").Option("missingkey=error").Funcs(funcs).Parse(source)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			execution := parsed.Execute(&output, tc.data)
			t.Logf("source=%q type=%s validation=%v registration=%v override=%v render=%v validation_calls=%d render_calls=%d content=%v oracle=%v output=%q", tc.source, spec.ArgsType, validation, registration, override, renderErr, validationCalls, renderCalls, content, execution, output.String())
			if validationCalls != 0 {
				t.Errorf("validation executed custom functions %d times", validationCalls)
			}
			if (execution == nil) != tc.want {
				t.Fatalf("oracle accepted=%v want=%v", execution == nil, tc.want)
			}
			for name, got := range map[string]error{"validation": validation, "registration": registration, "override": override} {
				if (got == nil) != tc.want {
					t.Errorf("%s accepted=%v want=%v", name, got == nil, tc.want)
				}
			}
			if tc.want && renderErr != nil {
				t.Errorf("valid render rejected: %v", renderErr)
			}
			if !tc.want && renderCalls != 0 {
				t.Errorf("invalid source executed custom emit before failure: %d", renderCalls)
			}
		})
	}
}

// TestQA293PointerCycles proves checking literal templates terminates for self
// and mutual pointer cycles; runtime execution sees only nil pointer values.
func TestQA293PointerCycles(t *testing.T) {
	t.Parallel()

	var self qa293Self
	var mutual qa293MutualA
	for _, data := range []any{self, mutual} {
		source := "literal"
		spec := PromptSpec{ID: "cycle", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(data)}
		if err := ValidatePromptTemplate(spec, source); err != nil {
			t.Fatal(err)
		}
		parsed := template.Must(template.New("oracle").Parse(source))
		var out bytes.Buffer
		if err := parsed.Execute(&out, data); err != nil {
			t.Fatal(err)
		}
		t.Logf("type=%T result=%q", data, out.String())
	}
}

// TestQA293PreservedInputs checks dynamic map names and composition retain
// their manual-prompt behavior, while typed prompts use the declared Go fields.
func TestQA293PreservedInputs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	reg := NewRegistry(nil)
	for _, spec := range []PromptSpec{
		{ID: "child", Role: PromptRoleSystem, Template: `{{.json_name}}`},
		{ID: "parent", Role: PromptRoleSystem, Template: `root:{{render "child" .}}`},
		{ID: "typed", Role: PromptRoleSystem, Template: `{{.GoName}}`, ArgsType: reflect.TypeFor[struct {
			GoName string `json:"json_name"`
		}]()},
	} {
		if err := reg.Register(spec); err != nil {
			t.Fatal(err)
		}
	}
	content, err := reg.Render(ctx, "parent", Scope{}, map[string]any{"json_name": "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if content.Text != "root:manual" || len(content.Refs) != 2 {
		t.Fatalf("manual map/composition changed: %+v", content)
	}
	if err := reg.ValidateOverride("typed", `{{.json_name}}`); err == nil {
		t.Fatal("JSON tag incorrectly treated as Go template field")
	}
	if err := reg.ValidateOverride("typed", `{{.Extra}}`); err == nil {
		t.Fatal("accepted field absent from declared type")
	}
	content, err = reg.Render(ctx, "typed", Scope{}, struct{ GoName, Extra string }{"declared", "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	if content.Text != "declared" {
		t.Fatalf("declared data result=%q", content.Text)
	}
	t.Log("manual JSON-name map and sub-prompt composition preserved; typed Go field contract preserved")
}
