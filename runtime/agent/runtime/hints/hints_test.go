package hints

import (
	"strings"
	"testing"
	"text/template"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCompileHintTemplates_Since(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		from    any
		to      any
		want    string
		wantErr bool
	}

	cases := []testCase{
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
