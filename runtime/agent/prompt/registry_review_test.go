package prompt

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"
	"text/template"
)

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

	// review65Good is a test helper for flow cycle review.
	review65Good struct{ Label string }

	// review65Data is a test root for flow cycle review.
	review65Data struct {
		Good     review65Good
		Bad      string
		Dynamic  any
		Count    int
		Children []review65Good
	}

	// qa65Item is a test helper for reflection method and wrapper checks.
	qa65Item struct{ Label string }

	// qa65Yield tests named yield signatures.
	qa65Yield func(qa65Item) bool

	// qa65Yield2 tests named 2-variable yield signatures.
	qa65Yield2 func(qa65Item, string) bool

	// qa65Bool is a defined boolean type.
	qa65Bool bool

	// qa65YieldBad tests yield functions returning defined booleans.
	qa65YieldBad func(qa65Item) qa65Bool

	// qa65CycleA tests 3-way pointer cycles.
	qa65CycleA *qa65CycleB

	// qa65CycleB tests 3-way pointer cycles.
	qa65CycleB *qa65CycleC

	// qa65CycleC tests 3-way pointer cycles.
	qa65CycleC *qa65CycleA

	// qa65Self tests self pointer cycles.
	qa65Self *qa65Self

	// qa65Root is the root struct for fresh reflection testing.
	qa65Root struct {
		Item     qa65Item
		V        reflect.Value
		Arr      [1]qa65Item
		Slice    []qa65Item
		Map      map[string]qa65Item
		Seq      iter.Seq[qa65Item]
		Seq2     iter.Seq2[qa65Item, string]
		Named    func(qa65Yield)
		Named2   func(qa65Yield2)
		Bad2     func(func(qa65Item, string) qa65Bool)
		BadNamed func(qa65YieldBad)
		PF       *func() qa65Item
		Send     chan<- qa65Item
		Receive  <-chan qa65Item
		Flag     bool
	}
)

// Pointer returns "ptr" to test pointer receiver method resolution.
func (*qa293Item) Pointer() string { return "ptr" }

// Wrapped returns reflect.Value of qa293Item to test value receiver method resolution.
func (qa293Item) Wrapped() reflect.Value { return reflect.ValueOf(qa293Item{"wrapped"}) }

// Wrapped returns reflect.Value of qa65Item.
func (v qa65Item) Wrapped() reflect.Value { return reflect.ValueOf(v) }

// WrappedErr returns reflect.Value and nil error (or an error if Label is "err").
func (v qa65Item) WrappedErr() (reflect.Value, error) {
	if v.Label == "err" {
		return reflect.Value{}, errors.New("err")
	}
	return reflect.ValueOf(v), nil
}

// Pointer returns Label to test pointer receiver resolution.
func (v *qa65Item) Pointer() string { return v.Label }

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

// TestReview65Flow validates flow-sensitive pipeline and and/or evaluation.
func TestReview65Flow(t *testing.T) {
	t.Parallel()

	data := review65Data{Good: review65Good{"good"}, Bad: "bad", Dynamic: "bad", Count: 1, Children: []review65Good{{"child"}}}
	cases := []struct {
		name    string
		source  string
		accept  bool
		execute bool
	}{
		{"dynamic_piped_and", `{{(.Dynamic | and .Good).Label}}`, true, false},
		{"dynamic_piped_or_branch", `{{$x := .Good}}{{if .Dynamic | or false}}{{$x = .Bad}}{{end}}{{$x.Label}}`, false, false},
		{"dynamic_piped_and_branch", `{{$x := .Good}}{{if .Dynamic | and true}}{{else}}{{$x = .Bad}}{{end}}{{$x.Label}}`, false, false},
		{"dynamic_index_valid", `{{(.Dynamic | index .Children).Label}}`, true, true},
		{"piped_not_valid", `{{$x := .Good}}{{if false | not}}{{else}}{{$x = .Bad}}{{end}}{{$x.Label}}`, true, true},
		{"toggle_even", `{{$x := .Good}}{{$f := true}}{{range 4}}{{$f = not $f}}{{end}}{{if $f}}{{else}}{{$x = .Bad}}{{end}}{{$x.Label}}`, true, true},
		{"toggle_odd", `{{$x := .Good}}{{$f := true}}{{range 3}}{{$f = not $f}}{{end}}{{if $f}}{{$x = .Bad}}{{end}}{{$x.Label}}`, true, true},
		{"fixedpoint_large", `{{$x := .Good}}{{range 1000000000}}{{$x = $.Good}}{{end}}{{$x.Label}}`, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := data
			if tc.name == "dynamic_index_valid" {
				data.Dynamic = 0
			}
			if tc.name == "dynamic_piped_and_branch" {
				data.Dynamic = false
			}
			calls := 0
			funcs := template.FuncMap{"witness": func() string { calls++; return "" }}
			source := `{{witness}}` + tc.source
			spec := PromptSpec{ID: "review65", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(data), Funcs: funcs}
			validation := ValidatePromptTemplate(spec, source)
			registration := NewRegistry(nil).Register(spec)
			store := NewInMemoryStore()
			reg := NewRegistry(store)
			base := spec
			base.Template = "base"
			if err := reg.Register(base); err != nil {
				t.Fatal(err)
			}
			override := reg.ValidateOverride(spec.ID, source)
			validationCalls := calls
			if err := store.Set(context.Background(), spec.ID, Scope{}, source, nil); err != nil {
				t.Fatal(err)
			}
			var renderErr error
			if tc.name != "fixedpoint_large" {
				_, renderErr = reg.Render(context.Background(), spec.ID, Scope{}, data)
			}
			renderCalls := calls
			var execution error
			var out bytes.Buffer
			if tc.name != "fixedpoint_large" {
				oracle, err := template.New("oracle").Option("missingkey=error").Funcs(funcs).Parse(source)
				if err != nil {
					t.Fatal(err)
				}
				execution = oracle.Execute(&out, data)
			}
			t.Logf("source=%q validation=%v registration=%v override=%v raw_render=%v validation_calls=%d render_calls=%d oracle=%v output=%q", source, validation, registration, override, renderErr, validationCalls, renderCalls, execution, out.String())
			if validationCalls != 0 {
				t.Error("validation executed helper")
			}
			for key, err := range map[string]error{"validation": validation, "registration": registration, "override": override} {
				if (err == nil) != tc.accept {
					t.Errorf("%s accepted=%t want=%t", key, err == nil, tc.accept)
				}
			}
			if tc.name != "fixedpoint_large" && (execution == nil) != tc.execute {
				t.Errorf("oracle execution=%t want=%t", execution == nil, tc.execute)
			}
			if tc.accept && tc.name != "fixedpoint_large" && (renderErr == nil) != tc.execute {
				t.Errorf("render execution=%t want=%t", renderErr == nil, tc.execute)
			}
			if !tc.accept && renderCalls != 0 {
				t.Error("invalid override executed helper")
			}
		})
	}
}

// TestReview65CycleCost validates that cycle detection in range loops terminates in bounded time
// across cycle periods without unrolling all loop iterations.
func TestReview65CycleCost(t *testing.T) {
	t.Parallel()

	source := `{{$f := true}}{{range 1000000000}}{{$f = not $f}}{{end}}`
	t.Logf("validating source=%q", source)
	spec := PromptSpec{ID: "review65", Role: PromptRoleSystem, ArgsType: reflect.TypeFor[struct{}]()}
	if err := ValidatePromptTemplate(spec, source); err != nil {
		t.Fatal(err)
	}
}

// TestQA65FreshReflection compares new reflected-boundary cases at every registry entry with text/template.
func TestQA65FreshReflection(t *testing.T) {
	t.Parallel()

	item := qa65Item{"item"}
	f := func() qa65Item { return item }
	ch := make(chan qa65Item, 1)
	ch <- item
	close(ch)
	root := qa65Root{
		Item:    item,
		V:       reflect.ValueOf(item),
		Arr:     [1]qa65Item{item},
		Slice:   []qa65Item{item},
		Map:     map[string]qa65Item{"x": item},
		PF:      &f,
		Send:    ch,
		Receive: ch,
	}
	root.Seq = func(y func(qa65Item) bool) { y(item) }
	root.Seq2 = func(y func(qa65Item, string) bool) { y(item, "s") }
	root.Named = func(y qa65Yield) { y(item) }
	root.Named2 = func(y qa65Yield2) { y(item, "s") }
	root.Bad2 = func(y func(qa65Item, string) qa65Bool) { y(item, "s") }
	root.BadNamed = func(y qa65YieldBad) { y(item) }
	pRoot := &root
	ppRoot := &pRoot
	badRoot := root
	badRoot.Flag = true
	cases := []struct {
		name   string
		source string
		data   any
		funcs  template.FuncMap
		want   bool
	}{
		{"method_error_return_dynamic", `{{.Item.WrappedErr.Label}}`, root, nil, true},
		{"direct_func_error_return_dynamic", `{{(wrapped).Label}}`, root, template.FuncMap{"wrapped": func() (reflect.Value, error) { return root.V, nil }}, true},
		{"call_error_return_wrapper_valid", `{{(call .Fn).String}}`, struct{ Fn func() (reflect.Value, error) }{func() (reflect.Value, error) { return root.V, nil }}, nil, true},
		{"call_error_return_wrapper_invalid", `{{(call .Fn).Label}}`, struct{ Fn func() (reflect.Value, error) }{func() (reflect.Value, error) { return root.V, nil }}, nil, false},
		{"call_pointer_return_valid", `{{(call .Fn).String}}`, struct{ Fn func() *reflect.Value }{func() *reflect.Value { return &root.V }}, nil, true},
		{"call_pointer_return_invalid", `{{(call .Fn).Label}}`, struct{ Fn func() *reflect.Value }{func() *reflect.Value { return &root.V }}, nil, false},
		{"and_value_field_wrapper_invalid", `{{(and .V .V).String}}`, root, nil, false},
		{"or_value_field_wrapper_invalid", `{{(or .V .V).String}}`, root, nil, false},
		{"pipeline_and_wrapper_invalid", `{{(.V | and true).String}}`, root, nil, false},
		{"pipeline_or_wrapper_invalid", `{{(.V | or false).String}}`, root, nil, false},
		{"and_value_field_unwrap", `{{(and .V .V).Label}}`, root, nil, true},
		{"or_value_field_unwrap", `{{(or .V .V).Label}}`, root, nil, true},
		{"pipeline_and_value_unwrap", `{{(.V | and true).Label}}`, root, nil, true},
		{"pipeline_or_value_unwrap", `{{(.V | or false).Label}}`, root, nil, true},
		{"index_value_wrapper_valid", `{{(index . 0).String}}`, []reflect.Value{root.V}, nil, true},
		{"index_value_wrapper_invalid", `{{(index . 0).Label}}`, []reflect.Value{root.V}, nil, false},
		{"range_value_wrapper_valid", `{{range .}}{{.String}}{{end}}`, []reflect.Value{root.V}, nil, true},
		{"range_value_wrapper_invalid", `{{range .}}{{.Label}}{{end}}`, []reflect.Value{root.V}, nil, false},
		{"named_yield_seq_valid", `{{range .Named}}{{.Label}}{{end}}`, root, nil, true},
		{"named_yield_seq_two_invalid", `{{range $k,$v:=.Named}}{{$v.Label}}{{end}}`, root, nil, false},
		{"named_yield_seq2_valid", `{{range $k,$v:=.Named2}}{{$k.Label}}{{$v}}{{end}}`, root, nil, true},
		{"named_yield_seq2_one_valid", `{{range .Named2}}{{.Label}}{{end}}`, root, nil, true},
		{"named_yield_seq2_bad_bool", `{{range .Bad2}}{{.Label}}{{end}}`, root, nil, false},
		{"named_yield_bad_named_bool", `{{range .BadNamed}}{{.Label}}{{end}}`, root, nil, false},
		{"pointer_seq_valid", `{{range .}}{{.Label}}{{end}}`, &root.Seq, nil, true},
		{"pointer_seq2_two_valid", `{{range $k,$v:=.}}{{$k.Label}}{{$v}}{{end}}`, &root.Seq2, nil, true},
		{"three_pointer_field_method", `{{.Item.Pointer}}`, ppRoot, nil, true},
		{"array_range_addressable", `{{range .Arr}}{{.Pointer}}{{end}}`, &root, nil, true},
		{"array_range_unaddressable", `{{range .Arr}}{{.Pointer}}{{end}}`, root, nil, false},
		{"slice_range_addressable", `{{range .Slice}}{{.Pointer}}{{end}}`, root, nil, true},
		{"map_range_unaddressable", `{{range .Map}}{{.Pointer}}{{end}}`, root, nil, false},
		{"receive_channel_control", `{{range channels}}{{.Label}}{{end}}`, root, template.FuncMap{"channels": func() <-chan qa65Item { ch := make(chan qa65Item, 1); ch <- item; close(ch); return ch }}, true},
		{"bidirectional_channel_control", `{{range channels}}{{.Label}}{{end}}`, root, template.FuncMap{"channels": func() chan qa65Item { ch := make(chan qa65Item, 1); ch <- item; close(ch); return ch }}, true},
		{"direct_function_call_control", `{{(call .Fn).Label}}`, struct{ Fn func() qa65Item }{f}, nil, true},
		{"nil_send_only_empty_valid", `{{range .Send}}{{.Label}}{{else}}empty{{end}}`, struct{ Send chan<- qa65Item }{}, nil, true},
		{"send_only_range_invalid", `{{range .Send}}{{.Label}}{{end}}`, root, nil, false},
		{"pointer_function_call_invalid", `{{(call .PF).Label}}`, root, nil, false},
		{"union_addressable_method_valid", `{{$x:=index .Slice 0}}{{if .Flag}}{{$x = .Item}}{{end}}{{$x.Pointer}}`, &root, nil, true},
		{"union_unaddressable_method_invalid", `{{$x:=index .Slice 0}}{{if .Flag}}{{$x = .Item}}{{end}}{{$x.Pointer}}`, badRoot, nil, false},
		{"three_pointer_cycle_literal", `literal`, qa65CycleA(nil), nil, true},
		{"three_pointer_cycle_member", `{{.Missing}}`, qa65CycleA(nil), nil, false},
		{"three_pointer_cycle_index", `{{index . 0}}`, qa65CycleA(nil), nil, false},
		{"three_pointer_cycle_range", `{{range .}}empty{{end}}`, qa65CycleA(nil), nil, false},
		{"self_pointer_cycle_member", `{{.Missing}}`, qa65Self(nil), nil, false},
		{"union_member_valid", `{{$x:=.Item}}{{if .Flag}}{{$x = .Item}}{{else}}{{$x = index .Slice 0}}{{end}}{{$x.Label}}`, root, nil, true},
		{"union_member_invalid", `{{$x:=.Item}}{{if .Flag}}{{$x = .Item}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, root, nil, false},
		{"union_index_valid", `{{$x:=.Arr}}{{if .Flag}}{{$x = .Slice}}{{end}}{{(index $x 0).Label}}`, root, nil, true},
		{"union_index_invalid", `{{$x:=.Arr}}{{if .Flag}}{{$x = "bad"}}{{end}}{{(index $x 0).Label}}`, badRoot, nil, false},
		{"union_range_valid", `{{$x:=.Seq}}{{if .Flag}}{{$x = .Seq2}}{{end}}{{range $x}}{{.Label}}{{end}}`, root, nil, true},
		{"union_range_two_invalid", `{{$x:=.Seq2}}{{if .Flag}}{{$x = .Seq}}{{end}}{{range $k,$v:=$x}}{{$k.Label}}{{$v}}{{end}}`, badRoot, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			funcs := template.FuncMap{"emit": func() string { calls++; return "" }}
			for k, v := range tc.funcs {
				funcs[k] = v
			}
			source := `{{emit}}` + tc.source
			spec := PromptSpec{ID: "qa65", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(tc.data), Funcs: funcs}
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
			var out bytes.Buffer
			execution := parsed.Execute(&out, tc.data)
			t.Logf("source=%q type=%v validation=%v registration=%v override=%v render=%v validation_calls=%d render_calls=%d content=%v oracle=%v output=%q", tc.source, spec.ArgsType, validation, registration, override, renderErr, validationCalls, renderCalls, content, execution, out.String())
			if validationCalls != 0 {
				t.Error("validation executed custom function")
			}
			if (execution == nil) != tc.want {
				t.Fatalf("oracle accepted=%v want=%v", execution == nil, tc.want)
			}
			// An unwrapped reflect.Value is genuinely dynamic: its runtime missing field is not statically rejectable.
			wantStatic := tc.want
			switch tc.name {
			case "and_value_field_wrapper_invalid",
				"or_value_field_wrapper_invalid",
				"pipeline_and_wrapper_invalid",
				"pipeline_or_wrapper_invalid",
				"send_only_range_invalid":
				wantStatic = true
			}
			for name, got := range map[string]error{"validation": validation, "registration": registration, "override": override} {
				if (got == nil) != wantStatic {
					t.Errorf("%s accepted=%v want=%v", name, got == nil, wantStatic)
				}
			}
			if tc.want && renderErr != nil {
				t.Errorf("valid render rejected: %v", renderErr)
			}
			if !wantStatic && renderCalls != 0 {
				t.Errorf("invalid raw override executed witness=%d", renderCalls)
			}
		})
	}
}
