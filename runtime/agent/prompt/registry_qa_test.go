package prompt

import (
	"bytes"
	"context"
	"iter"
	"reflect"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
)

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
