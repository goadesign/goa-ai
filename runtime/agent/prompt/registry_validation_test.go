package prompt

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"text/template"
)

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
