// Package codegen_test verifies emitted prompt generation, schema isolation, and template oracle validation.
package codegen_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
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

// TestIndependentBA23EmittedBackEdge checks emitted schemas and Go fields across recursive back-edges.
func TestIndependentBA23EmittedBackEdge(t *testing.T) {
	for _, kind := range []string{"tool", "completion", "prompt"} {
		t.Run(kind, func(t *testing.T) {
			files := testhelpers.BuildAndGenerateWithPkg(t, "review.local/gen", func() {
				goadsl.API("review", func() {})
				ancestor := goadsl.Type("Ancestor", func() {
					goadsl.Attribute("query", goadsl.String, "Query", func() { goadsl.MinLength(2) })
					goadsl.Required("query")
					goadsl.Attribute("back", "Node", "Back to node")
				})
				base := goadsl.Type("Base", func() { goadsl.Extend(ancestor) })
				derived := goadsl.Type("Derived", func() { goadsl.Extend(base) })
				node := goadsl.Type("Node", func() {
					goadsl.Attribute("self", "Node", "Self")
					goadsl.Attribute("nested", &goaexpr.Object{}, "Nested", func() { goadsl.Extend(derived) })
				})
				if kind == "tool" {
					tools := Toolset("queries", func() { Tool("query", "Query", func() { Args(node); Return(node) }) })
					goadsl.Service("support", func() { Agent("bot", "Query bot", func() { Use(tools) }) })
					return
				}
				goadsl.Service("support", func() {
					if kind == "prompt" {
						Prompt("main", func() { Template("ok"); Args(node) })
					} else {
						Completion("query", "Query", func() { Return(node) })
					}
				})
			})
			var schemaFound, goFieldFound bool
			for _, file := range files {
				if strings.HasSuffix(file.Path, "schemas.json") || strings.Contains(file.Path, "completions") || strings.HasSuffix(file.Path, "types.go") || strings.HasSuffix(file.Path, "prompts.go") {
					content := testhelpers.FileContent(t, files, file.Path)
					t.Logf("SNAPSHOT %s\n%s\nEND SNAPSHOT", file.Path, content)
					if strings.HasSuffix(file.Path, "schemas.json") && regexp.MustCompile(`"query"\s*:`).MatchString(content) {
						schemaFound = true
					}
					if regexp.MustCompile(`\bQuery\s+string\b`).MatchString(content) {
						goFieldFound = true
					}
				}
			}
			if kind != "completion" {
				assert.True(t, schemaFound, "published schema must contain inherited Query")
			}
			assert.True(t, goFieldFound, "generated contract must contain inherited Query")
			if kind == "prompt" {
				return
			}
			dir := t.TempDir()
			repo, err := filepath.Abs("../..")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module review.local\n\ngo 1.27.3\n\nrequire goa.design/goa-ai v0.0.0\nreplace goa.design/goa-ai => "+repo+"\n"), 0600))
			for _, file := range files {
				_, err := file.Render(dir)
				require.NoError(t, err)
			}
			packageName, packageDir, decoder := "queries", "gen/support/toolsets/queries", "SpecQuery().Payload.Codec.FromJSON"
			if kind == "completion" {
				packageName, packageDir, decoder = "completions", "gen/support/completions", "SpecQuery().Codec.FromJSON"
			}
			child := `package ` + packageName + `
import "testing"
func TestPreserveInheritedContract(t *testing.T){
	for _,tc:=range []struct{input string; valid bool}{
		{"{\"nested\":{}}",false},
		{"{\"nested\":{\"query\":\"a\"}}",false},
		{"{\"nested\":{\"query\":\"ab\"}}",true},
	}{
		_,err:=` + decoder + `([]byte(tc.input))
		t.Logf("input=%s err=%v",tc.input,err)
		if (err==nil)!=tc.valid{t.Errorf("input=%s want valid=%v, err=%v",tc.input,tc.valid,err)}
	}
}
`
			require.NoError(t, os.WriteFile(filepath.Join(dir, packageDir, "review_test.go"), []byte(child), 0600))
			ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1", "-timeout=25s", "-v", "./"+packageDir+"/...")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "CI=true", "GOWORK=off")
			output, err := cmd.CombinedOutput()
			t.Log(string(output))
			assert.NoError(t, err)
		})
	}
}

// TestIndependentBa23GeneratedContracts validates prompt template execution across generated types,
// ensuring slice and map operations preserve static truthiness contracts.
func TestIndependentBa23GeneratedContracts(t *testing.T) {
	forms := []string{
		`{{$x := .Item}}{{$v := or OPERAND "fallback"}}{{if slice $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`,
		`{{$x := .Item}}{{$v := or OPERAND "fallback"}}{{if $v | slice}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`,
		`{{$x := .Item}}{{$v := or OPERAND "fallback"}}{{range 2}}{{$v = slice $v}}{{end}}{{if $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}`,
		`{{define "n"}}{{$x := .Item}}{{$v := or OPERAND "fallback"}}{{if slice $v}}{{else}}{{$x = "bad"}}{{end}}{{$x.Label}}{{end}}{{template "n" .}}`,
	}
	modes := []string{
		"optional", "required", "defaulted",
		"named_optional", "named_required", "named_defaulted",
		"array", "named_array", "bytes", "map",
		"inherited_optional", "inherited_defaulted", "referenced_defaulted",
	}
	design := func(mode, source string) func() {
		return func() {
			goadsl.API("independent", func() {})
			item := goadsl.Type("Item", func() {
				goadsl.Attribute("label", goadsl.String, "Readable label")
				goadsl.Required("label")
			})
			namedString := goadsl.Type("Text", goadsl.String)
			namedArray := goadsl.Type("Texts", goadsl.ArrayOf(goadsl.String))
			base := goadsl.Type("Base", func() {
				goadsl.Attribute("operand", goadsl.String, "Inherited operand", func() {
					if strings.Contains(mode, "defaulted") {
						goadsl.Default("alpha")
					}
				})
			})
			goadsl.Service("review", func() {
				Agent("bot", "Review", func() {
					Prompt("triage", func() {
						Template(source)
						Args(func() {
							goadsl.Attribute("item", item, "Label source")
							goadsl.Required("item")
							switch mode {
							case "array":
								goadsl.Attribute("operand", goadsl.ArrayOf(goadsl.String), "Slice operand")
							case "named_array":
								goadsl.Attribute("operand", namedArray, "Named slice operand")
							case "bytes":
								goadsl.Attribute("operand", goadsl.Bytes, "Bytes operand")
							case "map":
								goadsl.Attribute("operand", goadsl.MapOf(goadsl.String, goadsl.String), "Map operand")
							case "inherited_optional", "inherited_defaulted":
								goadsl.Extend(base)
							case "referenced_defaulted":
								goadsl.Reference(base)
								goadsl.Attribute("operand")
							default:
								var typ goaexpr.DataType = goadsl.String
								if strings.HasPrefix(mode, "named_") {
									typ = namedString
								}
								goadsl.Attribute("operand", typ, "String operand", func() {
									if strings.Contains(mode, "defaulted") {
										goadsl.Default("alpha")
									}
								})
								if strings.Contains(mode, "required") {
									goadsl.Required("operand")
								}
							}
						})
					})
				})
			})
		}
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			valid := !strings.Contains(mode, "optional")
			for i, form := range forms {
				operand := ".Operand"
				if mode == "map" {
					operand = `(index .Operand "key")`
				}
				src := strings.ReplaceAll(form, "OPERAND", operand)
				testhelpers.SetupEvalRoots(t)
				require.True(t, eval.Execute(design(mode, src), nil), eval.Context.Error())
				compileErr := eval.RunDSL()
				t.Logf("DSL form=%d expectedValid=%t error=%v", i, valid, compileErr)
				if (compileErr == nil) != valid {
					t.Errorf("DSL/Go field disagreement mode=%s form=%d valid=%t error=%v", mode, i, valid, compileErr)
				}
			}
			files := testhelpers.BuildAndGenerateWithPkg(t, "independent.local/gen", design(mode, "ok"))
			dir := t.TempDir()
			for _, f := range files {
				_, err := f.Render(dir)
				require.NoError(t, err)
			}
			repo, err := filepath.Abs("../..")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module independent.local\n\ngo 1.27.3\nrequire goa.design/goa-ai v0.0.0\nreplace goa.design/goa-ai => "+repo+"\n"), 0600))
			generated, err := os.ReadFile(filepath.Join(dir, "gen/review/agents/bot/prompts.go")) //nolint:gosec // Read emitted test fixture.
			require.NoError(t, err)
			t.Logf("actual generated package mode=%s:\n%s", mode, generated)
			sources := make([]string, 0, len(forms))
			for _, form := range forms {
				operand := ".Operand"
				if mode == "map" {
					operand = `(index .Operand "key")`
				}
				sources = append(sources, fmt.Sprintf("%q", strings.ReplaceAll(form, "OPERAND", operand)))
			}
			child := fmt.Sprintf(`package bot_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"text/template"

	gen "independent.local/gen/review/agents/bot"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestGeneratedContract(t *testing.T) {
	valid := %t
	mode := %q
	spec := gen.PromptSpecTriage()
	v := reflect.New(spec.ArgsType).Elem()
	item := v.FieldByName("Item")
	item.Set(reflect.New(item.Type().Elem()))
	item.Elem().FieldByName("Label").SetString("alpha")
	operand := v.FieldByName("Operand")
	t.Logf("actual field type=%%s ArgsType=%%s", operand.Type(), spec.ArgsType)
	switch operand.Kind() {
	case reflect.Pointer:
		operand.Set(reflect.New(operand.Type().Elem()))
		operand.Elem().SetString("")
	case reflect.String:
		operand.SetString("alpha")
	case reflect.Slice:
		operand.Set(reflect.MakeSlice(operand.Type(), 1, 1))
		if operand.Type().Elem().Kind() == reflect.Uint8 {
			operand.Index(0).SetUint(65)
		} else {
			operand.Index(0).SetString("alpha")
		}
	case reflect.Map:
		operand.Set(reflect.MakeMap(operand.Type()))
		operand.SetMapIndex(reflect.ValueOf("key"), reflect.ValueOf("alpha"))
	default:
		t.Fatalf("unexpected operand %%s in %%s", operand.Type(), mode)
	}
	args := v.Interface()
	if reflect.TypeOf(args) != spec.ArgsType {
		t.Fatal("ArgsType mismatch")
	}
	for i, src := range []string{%s} {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			var output bytes.Buffer
			oracle := template.Must(template.New("oracle").Parse(src)).Execute(&output, args)
			t.Logf("oracle error=%%v output=%%q", oracle, output.String())
			if (oracle == nil) != valid {
				t.Fatalf("oracle validity mismatch %%v", oracle)
			}
			calls := 0
			spec.Template = "{{witness}}" + src
			spec.Funcs = template.FuncMap{"witness": func() string { calls++; return "" }}
			store := prompt.NewInMemoryStore()
			reg := prompt.NewRegistry(store)
			base := spec
			base.Template = "base"
			if e := reg.Register(base); e != nil {
				t.Fatal(e)
			}
			entries := []error{
				prompt.ValidatePromptTemplate(spec, spec.Template),
				prompt.NewRegistry(nil).Register(spec),
				prompt.NewRegistry(nil).ValidateSpecs([]prompt.PromptSpec{spec}),
				reg.ValidateOverride(spec.ID, spec.Template),
				reg.SetOverride(context.Background(), spec.ID, prompt.Scope{}, spec.Template, nil),
			}
			for j, e := range entries {
				if (valid && e != nil) || (!valid && !errors.Is(e, prompt.ErrTemplateFieldNotFound)) {
					t.Errorf("entry %%d=%%v", j, e)
				}
			}
			if calls != 0 {
				t.Fatal("validation called witness")
			}
			if e := store.Set(context.Background(), spec.ID, prompt.Scope{}, spec.Template, nil); e != nil {
				t.Fatal(e)
			}
			out, e := reg.Render(context.Background(), spec.ID, prompt.Scope{}, args)
			t.Logf("entries=%%v raw render=%%v out=%%v witness=%%d", entries, e, out, calls)
			if valid {
				if e != nil || out.Text != "alpha" || calls != 1 {
					t.Fatal("valid render failed", e, out, calls)
				}
			} else {
				if !errors.Is(e, prompt.ErrTemplateFieldNotFound) || calls != 0 {
					t.Fatal("invalid render reached witness", e, calls)
				}
			}
		})
	}
}
`, valid, mode, strings.Join(sources, ", "))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "gen/review/agents/bot/independent_test.go"), []byte(child), 0600))
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1", "-timeout=20s", "-v", "./...")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "CI=true", "GOWORK=off")
			out, err := cmd.CombinedOutput()
			t.Logf("complete generated module result:\n%s", out)
			require.NoError(t, err)
		})
	}
}
