// These checks exercise ordinary type discovery and the DSL import that
// registers generation, including unsupported values alongside valid ones.
package jsoncodec

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestUnlocatedOriginalsAndScopedForce(t *testing.T) {
	files, err := generate(t, func() {
		value := dsl.Type("Value", func() {
			dsl.Attribute("text", dsl.String)
			dsl.Required("text")
		})
		dsl.Type("FirstOnly", func() {
			dsl.Meta("type:generate:force", "first")
			dsl.Attribute("text", dsl.String)
		})
		dsl.Type("EncodeValue", dsl.String, func() {
			dsl.Meta("type:generate:force", "first")
		})
		for _, name := range []string{"first", "second"} {
			dsl.Service(name, func() {
				dsl.Method("check", func() { dsl.Payload(value) })
			})
		}
	})
	require.NoError(t, err)
	first := generatedSource(t, files, "gen/first")
	second := generatedSource(t, files, "gen/second")
	require.Contains(t, first, "func EncodeValue2(")
	require.Contains(t, first, "func DecodeValue(")
	require.Contains(t, first, "func EncodeFirstOnly(")
	require.Contains(t, second, "func EncodeValue(")
	require.NotContains(t, second, "FirstOnly")
	require.NotContains(t, generatedSource(t, files, "gen/catalog"), "FirstOnly")
	runGo(t, compileModule(t, files), "test", "./gen/...")
}

func TestUnsupportedCycleKeepsSupportedDescendant(t *testing.T) {
	files, err := generate(t, func() {
		child := dsl.Type("Child", func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Attribute("text", dsl.String)
		})
		located("CycleA", func() {
			dsl.Attribute("next", "CycleB")
			dsl.Attribute("dynamic", dsl.Any)
			dsl.Attribute("child", child)
		})
		located("CycleB", func() { dsl.Attribute("next", "CycleA") })
	})
	require.NoError(t, err)
	source := generatedSource(t, files, "gen/types")
	for _, name := range []string{"CycleA", "CycleB"} {
		require.Contains(t, source, "type "+name+" struct")
		require.NotContains(t, source, "func Encode"+name+"(")
		require.NotContains(t, source, "func Decode"+name+"(")
	}
	require.Contains(t, source, "func EncodeChild(")
	runGo(t, compileModule(t, files), "test", "./gen/...")
}

func TestUnsupportedOnlyPackageHasNoCodecDeclarations(t *testing.T) {
	files, err := generate(t, func() {
		located("Dynamic", func() { dsl.Attribute("value", dsl.Any) })
	})
	require.NoError(t, err)
	source := generatedSource(t, files, "gen/types")
	require.Contains(t, source, "type Dynamic struct")
	require.NotContains(t, source, "func EncodeDynamic(")
	require.NotContains(t, source, "func DecodeDynamic(")
	require.NotContains(t, source, "readStrictJSON")
	runGo(t, compileModule(t, files), "test", "./gen/...")
}

func TestFilesPreserveOriginalOwners(t *testing.T) {
	eval.Reset()
	expr.Root = new(expr.RootExpr)
	expr.GeneratedResultTypes = new(expr.ResultTypesRoot)
	require.NoError(t, eval.Register(expr.Root))
	require.NoError(t, eval.Register(expr.GeneratedResultTypes))
	dsl.API("codec", func() {})
	record := dsl.Type("Record", func() {
		dsl.Meta("struct:pkg:path", "APIKeyService")
		dsl.Attribute("text", dsl.String)
		dsl.Required("text")
	})
	dsl.Service("read_value", func() {
		dsl.Method("read", func() {
			dsl.Payload(func() { dsl.Attribute("record", record) })
		})
	})
	require.NoError(t, eval.RunDSL())
	roots, err := eval.Context.Roots()
	require.NoError(t, err)
	generation, err := codegen.NewGeneration("codec.local/gen", roots)
	require.NoError(t, err)
	services, err := service.NewPlan(expr.Root, generation, expr.NewExampleGenerator(expr.Root.API.RandomizerFactory))
	require.NoError(t, err)
	plan, err := NewPlan(generation)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.NoError(t, services.Link())
	files, err := service.Files(services)
	require.NoError(t, err)
	originals := append([]*codegen.File(nil), files...)
	sections := make(map[*codegen.File][]*codegen.SectionTemplate)
	paths := make(map[*codegen.File]string)
	bodies := make(map[*codegen.SectionTemplate]string)
	for _, file := range files {
		paths[file] = file.Path
		sections[file] = append([]*codegen.SectionTemplate(nil), file.SectionTemplates...)
		for _, section := range file.SectionTemplates {
			if section.Name == sourceHeader {
				continue
			}
			var body strings.Builder
			require.NoError(t, section.Write(&body))
			bodies[section] = body.String()
		}
	}
	files, err = plan.Files(append([]*codegen.File{nil}, files...))
	require.NoError(t, err)
	require.Nil(t, files[0], "the final Goa merger owns omission of empty entries")
	files = files[1:]
	require.Len(t, files, len(originals))
	for i, file := range files {
		require.Same(t, originals[i], file)
		require.Equal(t, paths[file], file.Path)
		for j, section := range sections[file] {
			require.Same(t, section, file.SectionTemplates[j])
			if section.Name == sourceHeader {
				continue
			}
			var body strings.Builder
			require.NoError(t, section.Write(&body))
			require.Equal(t, bodies[section], body.String())
		}
	}
	located := generatedSource(t, files, "gen/APIKeyService")
	require.Contains(t, located, "package apikeyservice")
	require.Equal(t, 1, strings.Count(located, "func EncodeRecord("))
	serviceSource := generatedSource(t, files, "gen/read_value")
	require.Contains(t, serviceSource, "package readvalue")
	require.Contains(t, serviceSource, "apikeyservice.Record")
	runGo(t, compileModule(t, files), "test", "./gen/...")
}

func TestFilesAllowEmptyEntriesWithoutCodecs(t *testing.T) {
	generation, err := codegen.NewGeneration("codec.local/gen", nil)
	require.NoError(t, err)
	owner, err := generation.ClaimPackage("codec.local/gen/types")
	require.NoError(t, err)
	dynamic := &expr.UserTypeExpr{
		TypeName: "Dynamic",
		UID:      "test:dynamic",
		AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{
			{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.Any}},
		}},
	}
	_, err = owner.DeclareUserType(dynamic)
	require.NoError(t, err)
	plan, err := NewPlan(generation)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	files, err := plan.Files([]*codegen.File{nil})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Nil(t, files[0])
}

func TestDSLImportsGenerateOriginalCodecs(t *testing.T) {
	for _, test := range []struct {
		name, imports, contracts, assertions string
	}{
		{
			name: "no agent",
			imports: `. "goa.design/goa/v3/dsl"
_ "goa.design/goa-ai/dsl"`,
			contracts: `var Input = Type("Input", func() {
 Attribute("text", String)
 Required("text")
})
var _ = Service("sample", func() {
 Method("check", func() { Payload(Input) })
})`,
			assertions: `var _ func(*sample.Input) ([]byte, error) = sample.EncodeInput
func TestOriginal(t *testing.T) {
 data, err := sample.EncodeInput(&sample.Input{Text: "ready"})
 if err != nil { t.Fatal(err) }
 value, err := sample.DecodeInput(data)
 if err != nil || value.Text != "ready" { t.Fatalf("%#v %v", value, err) }
}`,
		},
		{
			name: "tool and completion",
			imports: `. "goa.design/goa/v3/dsl"
. "goa.design/goa-ai/dsl"`,
			contracts: `var ToolInput = Type("ToolInput", func() {
 Attribute("text", String)
 Attribute("recordKey", String)
 Attribute("session_id", String)
 Required("text", "recordKey", "session_id")
})
var Item = Type("Item", func() {
 Meta("struct:pkg:path", "types")
 Attribute("text", String)
 Required("text")
})
var ItemList = Type("ItemList", ArrayOf(Item))
var Headline = Type("Headline", String)
var Lines = Type("Lines", ArrayOf(String))
var _ = Service("sample", func() {
 Agent("helper", "A synthetic helper.", func() {
  Use("checks", func() {
   Tool("check", "Check the supplied text.", func() {
    Args(ToolInput)
    Inject("session_id")
    Return(String)
   })
  })
 })
 Completion("items", "Return synthetic items.", func() {
  Return(ItemList)
 })
 Completion("headline", "Return synthetic text.", func() {
  Return(Headline)
 })
 Completion("lines", "Return synthetic lines.", func() {
  Return(Lines)
 })
})`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := compileModule(t, nil)
			module, err := filepath.Abs("../../..")
			require.NoError(t, err)
			runGo(t, root, "mod", "edit", "-require=goa.design/goa-ai@v0.0.0", "-replace=goa.design/goa-ai="+module)
			designDirectory := filepath.Join(root, "design")
			require.NoError(t, os.MkdirAll(designDirectory, 0o700)) // #nosec G703 -- root is the generated test module.
			design := "package design\nimport (\n" + test.imports + "\n)\nvar _ = API(\"codec\", func() {})\n" + test.contracts
			require.NoError(t, os.WriteFile(filepath.Join(designDirectory, "design.go"), []byte(design), 0o600)) // #nosec G703 -- fixed filename inside the test module.
			runGo(t, root, "mod", "tidy")
			runGo(t, root, "run", "goa.design/goa/v3/cmd/goa", "gen", "codec.local/design")
			if test.name == "no agent" {
				before := generatedGoTree(t, filepath.Join(root, "gen"))
				runGo(t, root, "run", "goa.design/goa/v3/cmd/goa", "example", "codec.local/design")
				require.Equal(t, before, generatedGoTree(t, filepath.Join(root, "gen")))
				starter, err := os.ReadFile(filepath.Join(root, "sample.go")) // #nosec G304 -- fixed generated filename in the private test module.
				require.NoError(t, err)
				require.NotContains(t, string(starter), "func EncodeInput(")
				require.NotContains(t, string(starter), "readStrictJSON")
			}
			fixture := "package sample_test\nimport (\n\"testing\"\nsample \"codec.local/gen/sample\"\n)\n" + test.assertions
			if test.name == "tool and completion" {
				source, err := os.ReadFile("testdata/protocol_coexistence_test.go")
				require.NoError(t, err)
				fixture = string(source)
				for _, source := range generatedGoTree(t, filepath.Join(root, "gen")) {
					for _, name := range []string{"ItemList", "Headline", "Lines", "ItemsResult", "HeadlineResult", "LinesResult"} {
						require.NotContains(t, source, "func Encode"+name+"(")
						require.NotContains(t, source, "func Decode"+name+"(")
					}
				}
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, "gen/sample/original_test.go"), []byte(fixture), 0o600)) // #nosec G703 -- fixed generated package in the test module.
			runGo(t, root, "mod", "tidy")
			runGo(t, root, "test", "./gen/...")
		})
	}
}

// generatedGoTree records source bytes by relative path so a later command must
// preserve both the generated file set and its contents.
func generatedGoTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		source, err := os.ReadFile(path) // #nosec G304,G122 -- this test owns the generated tree, with no symlinks or concurrent writers.
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = string(source)
		return nil
	}))
	return files
}
