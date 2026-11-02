// Package codegen_test verifies emitted prompt generation, schema isolation, and template oracle validation.
package codegen_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
)

// TestReviewBeaeEmittedReferenceExtend runs both emitted prompt forms and inspects their published schemas.
func TestReviewBeaeEmittedReferenceExtend(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "review.local/gen", func() {
		base := goadsl.Type("Base", func() {
			goadsl.Attribute("query", goadsl.String, "Base query", func() { goadsl.MinLength(2) })
			goadsl.Required("query")
		})
		ref := goadsl.Type("ReferenceDefaults", func() {
			goadsl.Attribute("query", goadsl.String, "Reference query", func() { goadsl.Default("ref-default") })
		})
		goadsl.API("review", func() {})
		goadsl.Service("support", func() {
			Prompt("mixed", func() {
				Template(`{{.Query}}`)
				Args(func() {
					goadsl.Reference(ref)
					goadsl.Extend(base)
					goadsl.Attribute("query", goadsl.String, "Consumer query")
				})
			})
			Prompt("plain", func() { Template(`{{.Query}}`); Args(base) })
		})
	})
	dir := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module review.local\n\ngo 1.27.3\n\nrequire goa.design/goa-ai v0.0.0\nreplace goa.design/goa-ai => "+repoRoot+"\n"), 0600))
	for _, f := range files {
		_, err := f.Render(dir)
		require.NoError(t, err)
	}
	schema, err := os.ReadFile(filepath.Join(dir, "gen/support/prompts/specs/prompt_schemas.json")) //nolint:gosec // Read emitted test fixture.
	require.NoError(t, err)
	t.Logf("emitted schema: %s", schema)
	var doc struct {
		Prompts []struct {
			Name string `json:"name"`
			Args struct {
				Schema struct {
					Properties map[string]struct {
						Default   json.RawMessage `json:"default"`
						MinLength int             `json:"minLength"` //nolint:tagliatelle // JSON Schema property minLength is camelCase.
					} `json:"properties"`
					Required []string `json:"required"`
				} `json:"schema"`
			} `json:"args"`
		} `json:"prompts"`
	}
	require.NoError(t, json.Unmarshal(schema, &doc))
	require.Len(t, doc.Prompts, 2)
	for _, p := range doc.Prompts {
		q, ok := p.Args.Schema.Properties["query"]
		require.True(t, ok, p.Name)
		assert.Empty(t, q.Default, p.Name)
		assert.Equal(t, 2, q.MinLength, p.Name)
		assert.Contains(t, p.Args.Schema.Required, "query", p.Name)
	}
	source := `package prompts
import("context";"testing";"goa.design/goa-ai/runtime/agent/prompt")
func TestEmittedBothPrompts(t *testing.T){
	reg:=prompt.NewRegistry(prompt.NewInMemoryStore());if err:=RegisterPrompts(reg);err!=nil{t.Fatal(err)}
	mixed,err:=RenderMixedPromptWithRegistry(context.Background(),reg,prompt.Scope{},MixedPromptArgs{Query:"ab"});if err!=nil{t.Fatal(err)}
	plain,err:=RenderPlainPromptWithRegistry(context.Background(),reg,prompt.Scope{},PlainPromptArgs{Query:"ab"});if err!=nil{t.Fatal(err)}
	if mixed.Text!="ab"||plain.Text!="ab"{t.Fatalf("mixed=%#v plain=%#v",mixed,plain)}
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gen/support/prompts/review_test.go"), []byte(source), 0600))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-v", "-timeout=20s", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CI=true", "GOWORK=off")
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	require.NoError(t, err)
}

// TestReviewA7cfGeneratedSlice compares DSL validation with actual generated optional string arguments.
func TestReviewA7cfGeneratedSlice(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		required     bool
	}{
		{"optional_direct", `{{$x := .Item}}{{$v := or .OptionalText "fallback"}}{{if slice $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false},
		{"optional_pipe", `{{$x := .Item}}{{$v := or .OptionalText "fallback"}}{{if $v | slice}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false},
		{"optional_loop", `{{$x := .Item}}{{$v := or .OptionalText "fallback"}}{{range 2}}{{$v = slice $v}}{{end}}{{if $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, false},
		{"optional_named", `{{define "n"}}{{$x := .Item}}{{$v := or .OptionalText "fallback"}}{{if slice $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}{{end}}{{template "n" .}}`, false},
		{"required_control", `{{$x := .Item}}{{$v := or .OptionalText "fallback"}}{{if slice $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			design := func(tmpl string) func() {
				return func() {
					goadsl.API("review_slice", func() {})
					item := goadsl.Type("SliceItem", func() {
						goadsl.Attribute("label", goadsl.String, "Readable label")
						goadsl.Required("label")
					})
					goadsl.Service("review", func() {
						Agent("bot", "Template review", func() {
							Prompt("triage", func() {
								Template(tmpl)
								Args(func() {
									goadsl.Attribute("item", item, "Label source")
									goadsl.Attribute("optional_text", goadsl.String, "Slice operand")
									goadsl.Required("item")
									if tc.required {
										goadsl.Required("optional_text")
									}
								})
							})
						})
					})
				}
			}

			if !tc.required {
				testhelpers.SetupEvalRoots(t)
				ok := eval.Execute(design(tc.source), nil)
				require.True(t, ok, eval.Context.Error())
				err := eval.RunDSL()
				require.Error(t, err)
				assert.Contains(t, err.Error(), `field "Label" not declared in type string`)
			}

			genTmpl := tc.source
			if !tc.required {
				genTmpl = "ok"
			}
			files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", design(genTmpl))
			root := t.TempDir()
			for _, file := range files {
				if strings.HasSuffix(file.Path, "/prompts.go") {
					_, err := file.Render(root)
					require.NoError(t, err)
				}
			}
			repo, err := filepath.Abs("../..")
			require.NoError(t, err)
			mod := fmt.Sprintf("module generated.local\n\ngo 1.27.3\n\nrequire goa.design/goa-ai v0.0.0\nreplace goa.design/goa-ai => %s\n", repo)
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0600))

			assignment := "empty := \"\"; args.OptionalText = &empty"
			if tc.required {
				assignment = "empty := \"\"; args.OptionalText = empty"
			}

			child := fmt.Sprintf(`package bot_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"text/template"

	gen "generated.local/gen/review/agents/bot"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestGeneratedOracle(t *testing.T) {
	args := gen.TriagePromptArgs{Item: &gen.SliceItem{Label: "ok"}}
	%s
	spec := gen.PromptSpecTriage()
	if spec.ArgsType != reflect.TypeOf(args) {
		t.Fatal("generated ArgsType mismatch")
	}
	var out bytes.Buffer
	oracle := template.Must(template.New("oracle").Parse(%q)).Execute(&out, args)
	registered := gen.RegisterPrompts(prompt.NewRegistry(nil))
	t.Logf("generated OptionalText kind=%%s oracle=%%v output=%%q registration=%%v", reflect.TypeOf(args.OptionalText), oracle, out.String(), registered)
	if %t {
		if oracle != nil || registered != nil || out.String() != "ok" {
			t.Fatal("required string positive control failed")
		}
		return
	}
	if oracle == nil || !strings.Contains(oracle.Error(), "can't evaluate field Label in type string") {
		t.Fatal("oracle did not reach intended field failure", oracle)
	}
	calls := 0
	spec.Template = "{{witness}}" + %q
	spec.Funcs = template.FuncMap{"witness": func() string { calls++; return "" }}
	store := prompt.NewInMemoryStore()
	reg := prompt.NewRegistry(store)
	baseline := spec
	baseline.Template = "base"
	if err := reg.Register(baseline); err != nil {
		t.Fatal(err)
	}
	entries := []error{
		prompt.ValidatePromptTemplate(spec, spec.Template),
		prompt.NewRegistry(nil).Register(spec),
		prompt.NewRegistry(nil).ValidateSpecs([]prompt.PromptSpec{spec}),
		reg.ValidateOverride(spec.ID, spec.Template),
		reg.SetOverride(context.Background(), spec.ID, prompt.Scope{}, spec.Template, nil),
	}
	for i, err := range entries {
		if !errors.Is(err, prompt.ErrTemplateFieldNotFound) {
			t.Fatalf("generated contract entry %%d=%%v", i, err)
		}
	}
	if calls != 0 {
		t.Fatal("validation executed witness")
	}
	if err := store.Set(context.Background(), spec.ID, prompt.Scope{}, spec.Template, nil); err != nil {
		t.Fatal(err)
	}
	_, renderErr := reg.Render(context.Background(), spec.ID, prompt.Scope{}, args)
	if !errors.Is(renderErr, prompt.ErrTemplateFieldNotFound) || calls != 0 {
		t.Fatal("raw-store render reached witness", renderErr, calls)
	}
	t.Logf("all generated-contract entries reject before witness: %%v render=%%v calls=%%d", entries, renderErr, calls)
}
`, assignment, tc.source, tc.required, tc.source)

			require.NoError(t, os.WriteFile(filepath.Join(root, "gen/review/agents/bot/review_test.go"), []byte(child), 0600))
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1", "-timeout=15s", "-v", "./...")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "CI=true", "GOWORK=off")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
		})
	}
}
