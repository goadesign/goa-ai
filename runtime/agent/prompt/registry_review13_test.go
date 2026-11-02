// Package prompt provides prompt template registration, evaluation, and rendering.
// This file tests review items for flow sensitivity, reflection boxing, and cycle limits.
package prompt

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"text/template"
)

type (
	// review13Item represents a test item with label and reflection helper methods.
	review13Item struct {
		Label string
	}

	// review13Data provides structured test arguments for template flow analysis.
	review13Data struct {
		Item     review13Item
		Dynamic  any
		Children []review13Item
		Fn       func() review13Item
		Flag     bool
	}

	// review13Root exercises reflection and pointer boundary conditions in template validation.
	review13Root struct {
		RV       reflect.Value
		PRV      *reflect.Value
		Item     review13Item
		Items    []review13Item
		Arr      [1]review13Item
		Wrappers []reflect.Value
		ByKey    map[string]reflect.Value
		Fn       func() review13Item
		PF       *func() review13Item
		FnRV     func() reflect.Value
		FnPRV    func() *reflect.Value
		Any      any
		Flag     bool
	}
)

// Pointer returns the item label via a pointer receiver to verify addressability requirements.
func (v *review13Item) Pointer() string {
	return v.Label
}

// Wrapped returns the item boxed in a reflect.Value to test dynamic unwrapping semantics.
func (v review13Item) Wrapped() reflect.Value {
	return reflect.ValueOf(v)
}

// TestReview13FreshFlow compares fresh expressions at every source entry without executing validation helpers.
func TestReview13FreshFlow(t *testing.T) {
	t.Parallel()

	d := review13Data{
		Item:     review13Item{"ok"},
		Dynamic:  true,
		Children: []review13Item{{"child"}},
		Fn:       func() review13Item { return review13Item{"fn"} },
	}
	cases := []struct {
		name    string
		source  string
		dynamic any
		accept  bool
	}{
		{"dynamic_or_guaranteed_true", `{{$x := .Item}}{{if or .Dynamic true}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false, true},
		{"dynamic_and_guaranteed_false", `{{$x := .Item}}{{if and .Dynamic false}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, true},
		{"piped_or_guaranteed_true", `{{$x := .Item}}{{if true | or .Dynamic}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false, true},
		{"piped_and_guaranteed_false", `{{$x := .Item}}{{if false | and .Dynamic}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, true},
		{"dynamic_not_or_guaranteed", `{{$x := .Item}}{{if not (or .Dynamic true)}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false, true},
		{"dynamic_or_unknown_reject", `{{$x := .Item}}{{if or .Dynamic false}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false, false},
		{"dynamic_and_unknown_reject", `{{$x := .Item}}{{if and .Dynamic true}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, false},
		{"index_dynamic_operand", `{{(.Dynamic | index .Children).Label}}`, 0, true},
		{"index_dynamic_missing", `{{(.Dynamic | index .Children).Missing}}`, 0, false},
		{"slice_dynamic_operand", `{{(index (.Dynamic | slice) 0).Label}}`, []review13Item{{"slice"}}, true},
		{"call_dynamic_operand", `{{(.Dynamic | call).Label}}`, d.Fn, true},
		{"call_explicit_dynamic_arg", `{{(.Dynamic | call .Fn).Label}}`, true, true},
		{"not_twice_pipe", `{{$x := .Item}}{{if false | not | not}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, true},
		{"and_or_custom_override", `{{(false | or .Item).Label}}`, true, true},
		{"named_bool_truth_environments", `{{define "t"}}{{$x := "bad"}}{{if .}}{{$x = $.Item}}{{end}}{{end}}{{template "t" true}}{{template "t" false}}`, true, false},
		{"named_dot_root_reset", `{{define "t"}}{{$.Label}}{{end}}{{template "t" .Item}}`, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := d
			data.Dynamic = tc.dynamic
			funcs := template.FuncMap{}
			if tc.name == "and_or_custom_override" {
				funcs["or"] = func(review13Item, bool) review13Item { return d.Item }
			}
			calls := 0
			funcs["witness"] = func() string { calls++; return "" }
			source := `{{witness}}` + tc.source
			spec := PromptSpec{ID: "review13", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(data), Funcs: funcs}
			errs := map[string]error{"validate": ValidatePromptTemplate(spec, source), "register": NewRegistry(nil).Register(spec)}
			store := NewInMemoryStore()
			reg := NewRegistry(store)
			base := spec
			base.Template = "base"
			if err := reg.Register(base); err != nil {
				t.Fatal(err)
			}
			errs["override"] = reg.ValidateOverride(spec.ID, source)
			if calls != 0 {
				t.Fatal("validation executed helper")
			}
			if err := store.Set(context.Background(), spec.ID, Scope{}, source, nil); err != nil {
				t.Fatal(err)
			}
			result, renderErr := reg.Render(context.Background(), spec.ID, Scope{}, data)
			renderCalls := calls
			oracle, err := template.New("oracle").Option("missingkey=error").Funcs(funcs).Parse(source)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			oracleErr := oracle.Execute(&out, data)
			t.Logf("source=%q static=%v raw_render=%v render_calls=%d result=%v oracle=%v output=%q", source, errs, renderErr, renderCalls, result, oracleErr, out.String())
			for name, err := range errs {
				if (err == nil) != tc.accept {
					t.Errorf("%s accepted=%t want=%t", name, err == nil, tc.accept)
				}
			}
			if !tc.accept && renderCalls != 0 {
				t.Error("rejected source executed witness")
			}
			if tc.accept && oracleErr == nil && renderErr != nil {
				t.Errorf("valid source blocked: %v", renderErr)
			}
		})
	}
}

// TestReview13ExactCycles exercises a period-three state transition, exact remainders, continues and breaks.
func TestReview13ExactCycles(t *testing.T) {
	t.Parallel()

	init := `{{$x := $.Item}}{{$a := true}}{{$b := false}}{{$c := false}}`
	rotate := `{{$old := $a}}{{$a = $b}}{{$b = $c}}{{$c = $old}}`
	for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 1000000000, 1000000001, 1000000002} {
		for _, cont := range []bool{false, true} {
			for _, valid := range []bool{false, true} {
				t.Run(fmt.Sprintf("n%d_continue%t_valid%t", n, cont, valid), func(t *testing.T) {
					cond := "$a"
					if n%3 == 1 {
						cond = "$c"
					}
					if n%3 == 2 {
						cond = "$b"
					}
					tail := `{{if ` + cond + `}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`
					if !valid {
						tail = `{{if ` + cond + `}}{{$x = "bad"}}{{end}}{{$x.Label}}`
					}
					body := rotate
					if cont {
						body += `{{continue}}{{$a = not $a}}`
					}
					src := init + fmt.Sprintf(`{{range %d}}`, n) + body + `{{end}}` + tail
					spec := PromptSpec{ID: "cycles", Role: PromptRoleSystem, ArgsType: reflect.TypeFor[review13Data]()}
					err := ValidatePromptTemplate(spec, src)
					if (err == nil) != valid {
						t.Errorf("source=%q err=%v expected valid=%t", src, err, valid)
					}
					if n < 10 {
						var out bytes.Buffer
						oracle := template.Must(template.New("oracle").Parse(src))
						oerr := oracle.Execute(&out, review13Data{Item: review13Item{"ok"}})
						if (oerr == nil) != valid {
							t.Fatalf("oracle=%v valid=%t", oerr, valid)
						}
					}
				})
			}
		}
	}
	for _, body := range []string{`{{break}}`, rotate + `{{if $a}}{{break}}{{end}}`, rotate + `{{if $a}}{{break}}{{else}}{{continue}}{{end}}`} {
		src := init + `{{range 1000000000}}` + body + `{{end}}{{if $a}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`
		if err := ValidatePromptTemplate(PromptSpec{ID: "break", ArgsType: reflect.TypeFor[review13Data]()}, src); err != nil {
			t.Errorf("break source=%q err=%v", src, err)
		}
	}
}

// TestReview13NamedTruth distinguishes both truth values for named templates sharing one Go boolean type.
func TestReview13NamedTruth(t *testing.T) {
	t.Parallel()

	for _, order := range []string{`true}}{{template "t" false`, `false}}{{template "t" true`} {
		src := `{{define "t"}}{{$x := item}}{{if .}}{{$x = "text"}}{{end}}{{$x.Label}}{{end}}{{template "t" ` + order + `}}`
		funcs := template.FuncMap{"item": func() review13Item { return review13Item{"ok"} }}
		err := ValidatePromptTemplate(PromptSpec{ID: "named", ArgsType: reflect.TypeFor[struct{}](), Funcs: funcs}, src)
		if err == nil || !strings.Contains(err.Error(), "Label") {
			t.Errorf("expected forbidden field on true input after false input: %v", err)
		}
	}
	src := `{{define "t"}}{{$x := item}}{{if .}}{{$x = "text"}}{{end}}{{$x.Label}}{{end}}{{template "t" false}}`
	if err := ValidatePromptTemplate(PromptSpec{ID: "named", ArgsType: reflect.TypeFor[struct{}](), Funcs: template.FuncMap{"item": func() review13Item { return review13Item{"ok"} }}}, src); err != nil {
		t.Fatalf("false input should preserve item: %v", err)
	}
}

// TestReview13CounterCost validates small authored binary counters without ever executing their billion-iteration template.
func TestReview13CounterCost(t *testing.T) {
	t.Parallel()

	for _, bits := range []int{6, 8, 10, 12} {
		t.Run(fmt.Sprintf("bits%d", bits), func(t *testing.T) {
			var init, body strings.Builder
			for i := 0; i < bits; i++ {
				fmt.Fprintf(&init, `{{$b%d := false}}`, i)
			}
			for i := 0; i < bits; i++ {
				fmt.Fprintf(&body, `{{$b%d = not $b%d}}`, i, i)
				if i < bits-1 {
					fmt.Fprintf(&body, `{{if not $b%d}}`, i)
				}
			}
			for i := 1; i < bits; i++ {
				body.WriteString(`{{end}}`)
			}
			source := init.String() + `{{range 1000000000}}` + body.String() + `{{end}}`
			t.Logf("bits=%d states=%d source_bytes=%d source=%q", bits, 1<<bits, len(source), source)
			if err := ValidatePromptTemplate(PromptSpec{ID: "cost", ArgsType: reflect.TypeFor[struct{}]()}, source); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestReview13Reflection validates reflection boxing and dynamic method invocation edge cases.
func TestReview13Reflection(t *testing.T) {
	t.Parallel()

	item := review13Item{"item"}
	rv := reflect.ValueOf(item)
	f := func() review13Item { return item }
	root := review13Root{
		RV:       rv,
		PRV:      &rv,
		Item:     item,
		Items:    []review13Item{item},
		Arr:      [1]review13Item{item},
		Wrappers: []reflect.Value{rv},
		ByKey:    map[string]reflect.Value{"x": rv},
		Fn:       f,
		PF:       &f,
		FnRV:     func() reflect.Value { return rv },
		FnPRV:    func() *reflect.Value { return &rv },
		Any:      item,
	}
	cases := []struct {
		name   string
		source string
		data   any
		static bool
		exec   bool
		out    string
		funcs  template.FuncMap
	}{
		{"direct_wrapper_string", `{{.RV.String}}`, root, true, true, "<prompt.review13Item Value>", nil},
		{"direct_wrapper_content_rejected", `{{.RV.Label}}`, root, false, false, "", nil},
		{"nested_and_or_content", `{{(and (or false .RV) true .RV).Label}}`, root, true, true, "item", nil},
		{"selected_map_index_content", `{{(or false (index .ByKey "x")).Label}}`, root, true, true, "item", nil},
		{"selected_slice_index_content", `{{(and true (index .Wrappers 0)).Label}}`, root, true, true, "item", nil},
		{"selected_wrapper_with", `{{with and true .RV}}{{.Label}}{{end}}`, root, true, true, "item", nil},
		{"selected_wrapper_named", `{{define "n"}}{{.Label}}{{end}}{{template "n" (or false .RV)}}`, root, true, true, "item", nil},
		{"selected_wrapper_variable", `{{$v:=or false .RV}}{{$v.Label}}`, root, true, true, "item", nil},
		{"selected_wrapper_unknown_member", `{{(or false .RV).Missing}}`, root, true, false, "", nil},
		{"pointer_wrapper_preserved", `{{(and true .PRV).String}}`, root, true, true, "<prompt.review13Item Value>", nil},
		{"pointer_wrapper_content_rejected", `{{(or false .PRV).Label}}`, root, false, false, "", nil},
		{"builtin_return_wrapper_selected", `{{(or false (call .FnRV)).Label}}`, root, true, true, "item", nil},
		{"builtin_return_wrapper_preserved", `{{(call .FnRV).String}}`, root, true, true, "<prompt.review13Item Value>", nil},
		{"builtin_return_wrapper_member_rejected", `{{(call .FnRV).Label}}`, root, false, false, "", nil},
		{"builtin_return_pointer_preserved", `{{(and true (call .FnPRV)).String}}`, root, true, true, "<prompt.review13Item Value>", nil},
		{"builtin_return_pointer_member_rejected", `{{(and true (call .FnPRV)).Label}}`, root, false, false, "", nil},
		{"direct_return_unwrapped", `{{(wrapped).Label}}`, root, true, true, "item", template.FuncMap{"wrapped": func() reflect.Value { return rv }}},
		{"direct_return_unwrapped_missing_dynamic", `{{(wrapped).Missing}}`, root, true, false, "", template.FuncMap{"wrapped": func() reflect.Value { return rv }}},
		{"method_return_selected", `{{(and true .Item.Wrapped).Label}}`, root, true, true, "item", nil},
		{"function_call_selected", `{{(call (and true .Fn)).Label}}`, root, true, true, "item", nil},
		{"pointer_function_call_selected_rejected", `{{(call (and true .PF)).Label}}`, root, false, false, "", nil},
		{"pointer_function_piped_rejected", `{{(.PF | call).Label}}`, root, false, false, "", nil},
		{"pointer_function_returned_rejected", `{{(call ptr).Label}}`, root, false, false, "", template.FuncMap{"ptr": func() *func() review13Item { return &f }}},
		{"custom_call_override", `{{(call .PF).Label}}`, root, true, true, "item", template.FuncMap{"call": func(*func() review13Item) review13Item { return item }}},
		{"custom_and_override_wrapper_preserved", `{{(and .RV).String}}`, root, true, true, "<prompt.review13Item Value>", template.FuncMap{"and": func(reflect.Value) any { return rv }}},
		{"index_wrapper_content_rejected", `{{(index .ByKey "x").Label}}`, root, false, false, "", nil},
		{"range_wrapper_string", `{{range .Wrappers}}{{.String}}{{end}}`, root, true, true, "<prompt.review13Item Value>", nil},
		{"range_wrapper_content_rejected", `{{range .Wrappers}}{{.Label}}{{end}}`, root, false, false, "", nil},
		{"selected_slice_addressable", `{{(and true (index .Items 0)).Pointer}}`, root, true, true, "item", nil},
		{"selected_array_unaddressable", `{{(and true (index .Arr 0)).Pointer}}`, root, false, false, "", nil},
		{"selected_array_addressable", `{{(or false (index .Arr 0)).Pointer}}`, &root, true, true, "item", nil},
		{"selected_field_addressable", `{{(and true .Item).Pointer}}`, &root, true, true, "item", nil},
		{"selected_field_unaddressable", `{{(or false .Item).Pointer}}`, root, false, false, "", nil},
		{"dynamic_any_selected", `{{(or false .Any).Label}}`, root, true, true, "item", nil},
		{"dynamic_any_missing_allowed", `{{(and true .Any).Missing}}`, root, true, false, "", nil},
		{"known_typed_missing_rejected", `{{(or false .Item).Missing}}`, root, false, false, "", nil},
		{"nil_send_only_preserved", `{{range .}}{{.Label}}{{else}}empty{{end}}`, (chan<- review13Item)(nil), true, true, "empty", nil},
		{"seq_pointer_method_rejected", `{{range .}}{{.Pointer}}{{end}}`, func(y func(review13Item) bool) { y(item) }, false, false, "", nil},
		{"seq_pointer_yield_preserved", `{{range .}}{{.Pointer}}{{end}}`, func(y func(*review13Item) bool) { y(&item) }, true, true, "item", nil},
		{"and_iterator_preserved", `{{range and true .}}{{.Label}}{{end}}`, func(y func(review13Item) bool) { y(item) }, true, true, "item", nil},
		{"root_reflect_value_content", `{{.Label}}`, rv, true, true, "item", nil},
		{"root_reflect_value_missing_dynamic", `{{.Missing}}`, rv, true, false, "", nil},
		{"nil_and_range_truth_preservation", `{{$x:=.Item}}{{range and nil .Items}}{{$x = "bad"}}{{end}}{{$x.Label}}`, root, true, true, "item", nil},
		{"nil_or_range_truth_preservation", `{{$x:=.Item}}{{range or nil nil}}{{$x = "bad"}}{{end}}{{$x.Label}}`, root, true, true, "item", nil},
		{"direct_nil_range_control", `{{$x:=.Item}}{{range nil}}{{$x = "bad"}}{{end}}{{$x.Label}}`, root, true, false, "", nil},
		{"named_nil_range_control", `{{define "n"}}{{$x:=item}}{{range .}}{{$x = "bad"}}{{end}}{{$x.Label}}{{end}}{{template "n"}}`, root, true, true, "item", template.FuncMap{"item": f}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			funcs := template.FuncMap{"witness": func() string { calls++; return "" }}
			for k, v := range tc.funcs {
				funcs[k] = v
			}
			source := `{{witness}}` + tc.source
			spec := PromptSpec{ID: "review13", Role: PromptRoleSystem, Template: source, ArgsType: reflect.TypeOf(tc.data), Funcs: funcs}
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
			oracle := parsed.Execute(&out, tc.data)
			t.Logf("source=%q type=%v validation=%v registration=%v override=%v render=%v validation_calls=%d render_calls=%d content=%v oracle=%v output=%q", tc.source, spec.ArgsType, validation, registration, override, renderErr, validationCalls, renderCalls, content, oracle, out.String())
			if validationCalls != 0 {
				t.Error("validation executed helper")
			}
			for name, e := range map[string]error{"validation": validation, "registration": registration, "override": override} {
				if (e == nil) != tc.static {
					t.Errorf("%s accepted=%v want=%v", name, e == nil, tc.static)
				}
			}
			if (oracle == nil) != tc.exec {
				t.Errorf("oracle accepted=%v want=%v", oracle == nil, tc.exec)
			}
			if tc.exec && out.String() != tc.out {
				t.Errorf("oracle output=%q want=%q", out.String(), tc.out)
			}
			if tc.exec && (renderErr != nil || content == nil || content.Text != out.String()) {
				t.Errorf("render differs from oracle")
			}
			if !tc.static && renderCalls != 0 {
				t.Errorf("invalid override executed witness=%d", renderCalls)
			}
		})
	}
}

// TestReview8722FreshDynamicPropagation checks dynamic type, truth, and side effects at all source entries.
func TestReview8722FreshDynamicPropagation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
		accept bool
		funcs  template.FuncMap
	}{
		{"merge_true", `{{$x := .Item}}{{$v := .Dynamic}}{{if .Flag}}{{$v = or .Dynamic true}}{{else}}{{$v = or .Other "yes"}}{{end}}{{if $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, nil},
		{"merge_false", `{{$x := .Item}}{{$v := .Dynamic}}{{if .Flag}}{{$v = and .Dynamic false}}{{else}}{{$v = and .Other ""}}{{end}}{{if $v}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, nil},
		{"merge_unknown", `{{$x := .Item}}{{$v := .Dynamic}}{{if .Flag}}{{$v = or .Dynamic true}}{{else}}{{$v = and .Other false}}{{end}}{{if $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false, nil},
		{"named_dynamic_true", `{{define "t"}}{{$x := item}}{{if $}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}{{end}}{{template "t" (or .Dynamic true)}}`, true, nil},
		{"named_dynamic_false", `{{define "t"}}{{$x := item}}{{if $}}{{$x = "bad"}}{{end}}{{$x.Label}}{{end}}{{template "t" (and .Dynamic false)}}`, true, nil},
		{"loop_dynamic_true", `{{$x := .Item}}{{$v := or .Dynamic true}}{{range 4}}{{$v = not $v}}{{end}}{{if $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, nil},
		{"loop_dynamic_false_continue", `{{$x := .Item}}{{$v := and .Dynamic false}}{{range 5}}{{$v = not $v}}{{continue}}{{$v = not $v}}{{end}}{{if $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, nil},
		{"loop_dynamic_break", `{{$x := .Item}}{{$v := or .Dynamic true}}{{range 4}}{{$v = not $v}}{{break}}{{end}}{{if $v}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, nil},
		{"custom_not_not_constant", `{{$x := .Item}}{{if not (or .Dynamic true)}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false, template.FuncMap{"not": func(any) bool { return true }}},
		{"dynamic_slice_truth_not_assumed", `{{$x := .Item}}{{$v := or .Dynamic true}}{{if slice $v 0 0}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false, nil},
		{"dynamic_slice_no_indices_preserves_truth", `{{$x := .Item}}{{$v := or .Dynamic true}}{{if slice $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			type item struct{ Label string }
			type data struct {
				Item    item
				Dynamic any
				Other   any
				Flag    bool
			}
			d := data{Item: item{"ok"}, Dynamic: []int{1}, Other: false, Flag: true}
			if c.name == "dynamic_slice_truth_not_assumed" || c.name == "dynamic_slice_no_indices_preserves_truth" {
				d.Dynamic = []int{1}
			}
			calls := 0
			funcs := template.FuncMap{
				"witness": func() string { calls++; return "" },
				"item":    func() item { return d.Item },
			}
			for k, v := range c.funcs {
				funcs[k] = v
			}
			src := `{{witness}}` + c.source
			spec := PromptSpec{ID: "fresh8722", Role: PromptRoleSystem, Template: src, ArgsType: reflect.TypeOf(d), Funcs: funcs}
			errs := make([]error, 0, 3)
			errs = append(errs, ValidatePromptTemplate(spec, src), NewRegistry(nil).Register(spec))
			store := NewInMemoryStore()
			reg := NewRegistry(store)
			base := spec
			base.Template = "base"
			if err := reg.Register(base); err != nil {
				t.Fatal(err)
			}
			errs = append(errs, reg.ValidateOverride(spec.ID, src))
			if calls != 0 {
				t.Fatal("validation ran witness")
			}
			if err := store.Set(context.Background(), spec.ID, Scope{}, src, nil); err != nil {
				t.Fatal(err)
			}
			content, render := reg.Render(context.Background(), spec.ID, Scope{}, d)
			renderCalls := calls
			oracle := template.Must(template.New("oracle").Option("missingkey=error").Funcs(funcs).Parse(src))
			var out bytes.Buffer
			exec := oracle.Execute(&out, d)
			t.Logf("source=%q static=%v render=%v calls=%d content=%v oracle=%v output=%q", src, errs, render, renderCalls, content, exec, out.String())
			for _, err := range errs {
				if (err == nil) != c.accept {
					t.Errorf("accepted=%v want=%v err=%v", err == nil, c.accept, err)
				}
			}
			if !c.accept && renderCalls != 0 {
				t.Error("invalid raw override ran witness")
			}
			if c.accept && (exec != nil || render != nil || content.Text != out.String()) {
				t.Error("valid execution does not match oracle")
			}
		})
	}
}

// TestReview8722CounterField validates a compact counter whose final truth chooses a legal field.
func TestReview8722CounterField(t *testing.T) {
	t.Parallel()

	var init, body strings.Builder
	init.WriteString(`{{$x := .Item}}`)
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&init, `{{$b%d := false}}`, i)
	}
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&body, `{{$b%d = not $b%d}}`, i, i)
		if i < 11 {
			fmt.Fprintf(&body, `{{if not $b%d}}`, i)
		}
	}
	for i := 1; i < 12; i++ {
		body.WriteString(`{{end}}`)
	}
	src := init.String() + `{{range 1000000000}}` + body.String() + `{{end}}{{if $b9}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`
	// One billion modulo 4096 is 2560, whose bit 9 is true. Do not execute this template.
	t.Logf("source_bytes=%d states=4096 source=%q", len(src), src)
	type item struct{ Label string }
	type data struct{ Item item }
	if err := ValidatePromptTemplate(PromptSpec{ID: "counter-field", ArgsType: reflect.TypeFor[data]()}, src); err != nil {
		t.Fatal(err)
	}
}
