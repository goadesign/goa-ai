package hints

import (
	"strings"
	"testing"
	"text/template"
	"time"

	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	timestampText     string
	timestampStringer string
)

func (t timestampStringer) String() string {
	return "2025-01-01T00:00:00Z"
}

func TestCompileHintTemplates_Since(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		from    any
		to      any
		want    string
		wantErr bool
	}

	from := timestampText("2025-01-01T00:00:00.125Z")
	to := timestampText("2025-01-01T00:00:10.125Z")
	cases := []testCase{
		{
			name: "named_string_pointers",
			from: &from,
			to:   &to,
			want: "10",
		},
		{
			name: "rfc3339_z",
			from: "2025-01-01T00:00:00Z",
			to:   "2025-01-01T00:00:10Z",
			want: "10",
		},
		{
			name: "rfc3339_offset_negative",
			from: "2025-01-01T00:00:00-08:00",
			to:   "2025-01-01T00:00:00Z",
			want: "-28800",
		},
		{
			name: "invalid_returns_zero",
			from: "not-a-time",
			to:   "2025-01-01T00:00:00Z",
			want: "0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := map[tools.Ident]string{
				tools.Ident("t"): "{{ since .From .To }}",
			}
			compiled, err := CompileHintTemplates(raw, template.FuncMap{})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CompileHintTemplates error: %v", err)
			}

			tmpl := compiled[tools.Ident("t")]
			if tmpl == nil {
				t.Fatalf("expected compiled template")
			}

			var b strings.Builder
			if err := tmpl.Execute(&b, map[string]any{
				"From": tc.from,
				"To":   tc.to,
			}); err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if got := strings.TrimSpace(b.String()); got != tc.want {
				t.Fatalf("unexpected output: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCompileHintTemplates_HumanTime(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		in   any
		want string
	}

	named := timestampText("2025-01-01T00:00:00Z")
	cases := []testCase{
		{
			name: "named_string",
			in:   named,
			want: "Jan 1, 12:00 AM",
		},
		{
			name: "named_string_pointer",
			in:   &named,
			want: "Jan 1, 12:00 AM",
		},
		{
			name: "nil_named_string_pointer",
			in:   (*timestampText)(nil),
			want: "<nil>",
		},
		{
			name: "rfc3339_z",
			in:   "2025-01-01T00:00:00Z",
			want: "Jan 1, 12:00 AM",
		},
		{
			name: "rfc3339_offset",
			in:   "2025-01-01T00:00:00-08:00",
			want: "Jan 1, 12:00 AM",
		},
		{
			name: "invalid_falls_back_to_input",
			in:   "not-a-time",
			want: "not-a-time",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := map[tools.Ident]string{
				tools.Ident("t"): "{{ humanTime .At }}",
			}
			compiled, err := CompileHintTemplates(raw, template.FuncMap{})
			if err != nil {
				t.Fatalf("CompileHintTemplates error: %v", err)
			}

			tmpl := compiled[tools.Ident("t")]
			if tmpl == nil {
				t.Fatalf("expected compiled template")
			}

			var b strings.Builder
			if err := tmpl.Execute(&b, map[string]any{
				"At": tc.in,
			}); err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if got := strings.TrimSpace(b.String()); got != tc.want {
				t.Fatalf("unexpected output: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCompileHintTemplates_NumberFormatsNumericPointers(t *testing.T) {
	t.Parallel()

	temperature := 82.75
	count := 38
	raw := map[tools.Ident]string{
		tools.Ident("t"): `{{printf "%.0f" (number .Temperature)}}F and {{printf "%.0f" (number .Count)}} periods`,
	}
	compiled, err := CompileHintTemplates(raw, template.FuncMap{})
	if err != nil {
		t.Fatalf("CompileHintTemplates error: %v", err)
	}

	tmpl := compiled[tools.Ident("t")]
	if tmpl == nil {
		t.Fatalf("expected compiled template")
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]any{
		"Temperature": &temperature,
		"Count":       &count,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := strings.TrimSpace(b.String()); got != "83F and 38 periods" {
		t.Fatalf("unexpected output: got %q want %q", got, "83F and 38 periods")
	}
}

func TestCompileHintTemplates_NumberRejectsNonNumericValues(t *testing.T) {
	t.Parallel()

	raw := map[tools.Ident]string{
		tools.Ident("t"): `{{number .Value}}`,
	}
	compiled, err := CompileHintTemplates(raw, template.FuncMap{})
	if err != nil {
		t.Fatalf("CompileHintTemplates error: %v", err)
	}

	tmpl := compiled[tools.Ident("t")]
	if tmpl == nil {
		t.Fatalf("expected compiled template")
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]any{"Value": "warm"}); err == nil {
		t.Fatalf("expected Execute error")
	}
}

func TestRenderResultHintReturnsTemplateErrors(t *testing.T) {
	t.Parallel()

	toolID := tools.Ident("test.render_result_error")
	compiled, err := CompileHintTemplates(map[tools.Ident]string{
		toolID: `{{.Missing}}`,
	}, template.FuncMap{})
	if err != nil {
		t.Fatalf("CompileHintTemplates error: %v", err)
	}
	RegisterResultHints(compiled)

	_, ok, err := RenderResultHint(toolID, map[string]any{})
	if !ok {
		t.Fatalf("expected registered result hint")
	}
	if err == nil {
		t.Fatalf("expected render error")
	}
}

func TestRenderResultHintRejectsEmptyOutput(t *testing.T) {
	t.Parallel()

	toolID := tools.Ident("test.render_result_empty")
	compiled, err := CompileHintTemplates(map[tools.Ident]string{
		toolID: `{{""}}`,
	}, template.FuncMap{})
	if err != nil {
		t.Fatalf("CompileHintTemplates error: %v", err)
	}
	RegisterResultHints(compiled)

	_, ok, err := RenderResultHint(toolID, map[string]any{})
	if !ok {
		t.Fatalf("expected registered result hint")
	}
	if err == nil {
		t.Fatalf("expected empty output error")
	}
}

func TestParseTimestamp(t *testing.T) {
	t.Parallel()

	text := "2025-01-01T00:00:00.123456789-08:00"
	want, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	named := timestampText(text)
	invalid := timestampText("not-a-time")
	empty := timestampText("")
	stringer := timestampStringer("not-a-time")
	stringerWant := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   any
		want time.Time
		ok   bool
	}{
		{name: "string", in: text, want: want, ok: true},
		{name: "string_pointer", in: &text, want: want, ok: true},
		{name: "named_string", in: named, want: want, ok: true},
		{name: "named_string_pointer", in: &named, want: want, ok: true},
		{name: "time", in: want, want: want, ok: true},
		{name: "time_pointer", in: &want, want: want, ok: true},
		{name: "stringer", in: stringer, want: stringerWant, ok: true},
		{name: "stringer_pointer", in: &stringer, want: stringerWant, ok: true},
		{name: "nil", in: nil},
		{name: "nil_time_pointer", in: (*time.Time)(nil)},
		{name: "nil_string_pointer", in: (*string)(nil)},
		{name: "nil_named_string_pointer", in: (*timestampText)(nil)},
		{name: "empty_named_string", in: empty},
		{name: "empty_named_string_pointer", in: &empty},
		{name: "invalid_named_string", in: invalid},
		{name: "invalid_named_string_pointer", in: &invalid},
		{name: "unsupported_number", in: 123},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseTimestamp(tc.in)
			if ok != tc.ok || !got.Equal(tc.want) || got.Nanosecond() != tc.want.Nanosecond() {
				t.Fatalf("parseTimestamp(%T) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
