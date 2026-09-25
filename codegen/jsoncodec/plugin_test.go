// These tests compile the generated API and exercise strictness through its
// public typed functions, alongside selection and original-type ownership.
package jsoncodec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestGeneratedStandaloneCodec(t *testing.T) {
	files, err := generate(t, settingsDesign)
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/codec_test.go.txt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/jsoncodec/codec_test.go"), source, 0o600)) // #nosec G703 -- root is this test's private temporary directory.
	reader, err := os.ReadFile("testdata/strict_reader_test.go.txt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/jsoncodec/internal/codec/strict_reader_test.go"), reader, 0o600)) // #nosec G703 -- root is this test's private temporary directory.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}

func TestSelectionErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		design func()
		want   string
	}{
		{"values", func() { dsl.Type("Settings", func() { dsl.Meta(selectionKey, "true") }) }, "takes no values"},
		{"unlocated", func() { dsl.Type("Settings", func() { dsl.Meta(selectionKey) }) }, "explicit generated package"},
		{"unemitted", func() {
			dsl.Type("Settings", func() { dsl.Meta(selectionKey); dsl.Meta("struct:pkg:path", "types") })
		}, "not generated"},
		{"field", func() {
			dsl.Type("Settings", func() { dsl.Attribute("title", dsl.String, func() { dsl.Meta(selectionKey) }) })
		}, "only valid on a named type"},
		{"service", func() { dsl.Service("extra", func() { dsl.Meta(selectionKey) }) }, "only valid on a named type"},
		{"method", func() { dsl.Service("extra", func() { dsl.Method("read", func() { dsl.Meta(selectionKey) }) }) }, "only valid on a named type"},
		{"any", func() { located("Settings", func() { dsl.Meta(selectionKey); dsl.Attribute("value", dsl.Any) }) }, "Any has no closed"},
		{"custom", func() {
			located("Settings", func() {
				dsl.Meta(selectionKey)
				dsl.Attribute("value", dsl.Bytes, func() {
					dsl.Meta("struct:field:type", "json.RawMessage", "encoding/json")
				})
			})
		}, "custom Go representation"},
		{"nonstringmap", func() {
			located("Settings", func() { dsl.Meta(selectionKey); dsl.Attribute("value", dsl.MapOf(dsl.Int, dsl.String)) })
		}, "map keys must"},
		{"outputowner", func() {
			located("Settings", func() { dsl.Meta(selectionKey); dsl.Attribute("title", dsl.String) })
			dsl.Type("Occupied", func() {
				dsl.Meta("struct:pkg:path", "types/jsoncodec")
				dsl.Meta("type:generate:force")
				dsl.Attribute("value", dsl.String)
			})
		}, "already owned"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := generate(t, test.design)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestOnlySelectedOriginalReceivesAPI(t *testing.T) {
	files, err := generate(t, func() {
		base := located("Base", func() { dsl.Meta(selectionKey); dsl.Attribute("title", dsl.String) })
		located("Derived", func() { dsl.Extend(base); dsl.Attribute("extra", dsl.String) })
		located("Parent", func() { dsl.Attribute("left", base); dsl.Attribute("right", base) })
	})
	require.NoError(t, err)
	var api string
	for _, file := range files {
		if filepath.ToSlash(file.Path) == "gen/types/jsoncodec/codec.go" {
			var text strings.Builder
			for _, section := range file.SectionTemplates {
				require.NoError(t, section.Write(&text))
			}
			api = text.String()
		}
	}
	require.Equal(t, 1, strings.Count(api, "func EncodeBase("))
	require.Equal(t, 1, strings.Count(api, "func DecodeBase("))
	require.NotContains(t, api, "EncodeDerived")
	require.NotContains(t, api, "EncodeParent")
}

func TestUnselectedAddsNothing(t *testing.T) {
	files, err := generate(t, func() { located("Settings", func() { dsl.Attribute("title", dsl.String) }) })
	require.NoError(t, err)
	for _, file := range files {
		require.NotContains(t, filepath.ToSlash(file.Path), "/jsoncodec/")
	}
}

func TestLocatedRootsAndImports(t *testing.T) {
	files, err := generate(t, func() {
		for _, name := range []string{"alpha", "json"} {
			dsl.Type(name+"Record", func() {
				dsl.Meta("struct:pkg:path", name)
				dsl.Meta("struct:type:name", name+"Record")
				dsl.Meta("type:generate:force")
				dsl.Meta(selectionKey)
				dsl.Attribute("Text", dsl.String)
				dsl.Required("Text")
			})
		}
		dsl.Type("Word", dsl.String, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Meta(selectionKey)
		})
		dsl.Type("Words", dsl.ArrayOf(dsl.String), func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Meta(selectionKey)
		})
		dsl.Type("Labels", dsl.MapOf(dsl.String, dsl.String), func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Meta(selectionKey)
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	runGo(t, root, "test", "./gen/...")
}

// settingsDesign uses only synthetic values; the full graph is schema-owned.
func settingsDesign() {
	text := located("Text", func() { dsl.Attribute("value", dsl.String); dsl.Required("value") })
	node := located("Node", func() {
		dsl.Attribute("Label", dsl.String)
		dsl.Attribute("Count", dsl.Int, func() { dsl.Minimum(0) })
		dsl.Attribute("Next", "Node")
		dsl.Attribute("Entries", dsl.ArrayOf("Node"))
		dsl.Attribute("RequiredEntries", dsl.ArrayOfRequired("Node"))
		dsl.Attribute("Children", dsl.MapOf(dsl.String, "Node"))
		dsl.OneOf("Link", func() {
			dsl.Attribute("node", "Node")
			dsl.Attribute("text", dsl.String)
		})
		dsl.Required("Label", "Count")
	})
	located("Settings", func() {
		dsl.Meta(selectionKey)
		dsl.Attribute("Enabled", dsl.Boolean, func() { dsl.Default(false) })
		dsl.Attribute("Count", dsl.Int, func() { dsl.Minimum(0) })
		dsl.Attribute("Title", dsl.String, func() { dsl.MinLength(1) })
		dsl.Attribute("Node", node)
		dsl.Attribute("Nodes", dsl.ArrayOf(node))
		dsl.Attribute("RequiredNodes", dsl.ArrayOfRequired(node))
		dsl.Attribute("Labels", dsl.MapOf(dsl.String, dsl.String))
		dsl.OneOf("Choice", func() {
			dsl.Attribute("text", text)
			dsl.Attribute("number", dsl.Int)
			dsl.Attribute("node", node)
		})
		dsl.Required("Enabled", "Count", "Title", "Node", "Choice")
	})
}

// located mirrors ordinary Goa ownership; the codec never forces declarations.
func located(name string, body func()) expr.UserType {
	return dsl.Type(name, func() {
		dsl.Meta("struct:pkg:path", "types")
		dsl.Meta("type:generate:force")
		body()
	})
}

// generate follows the real core/plugin planning lifecycle without a service method.
func generate(t *testing.T, design func()) ([]*codegen.File, error) {
	t.Helper()
	eval.Reset()
	expr.Root = new(expr.RootExpr)
	expr.GeneratedResultTypes = new(expr.ResultTypesRoot)
	require.NoError(t, eval.Register(expr.Root))
	require.NoError(t, eval.Register(expr.GeneratedResultTypes))
	dsl.API("codec", func() {})
	dsl.Service("catalog", func() {})
	design()
	require.NoError(t, eval.RunDSL())
	roots, err := eval.Context.Roots()
	require.NoError(t, err)
	generation, err := codegen.NewGeneration("codec.local/gen", roots)
	require.NoError(t, err)
	services, err := service.NewPlan(expr.Root, generation, expr.NewExampleGenerator(expr.Root.API.RandomizerFactory))
	if err != nil {
		return nil, err
	}
	p := new(plugin)
	if err := p.plan(generation); err != nil {
		return nil, err
	}
	if err := generation.Freeze(); err != nil {
		return nil, err
	}
	if err := services.Link(); err != nil {
		return nil, err
	}
	files, err := service.Files(services)
	if err != nil {
		return nil, err
	}
	return p.generate(files)
}

// compileModule uses the repository's pinned dependency graph in a throwaway module.
func compileModule(t *testing.T, files []*codegen.File) string {
	t.Helper()
	root := t.TempDir()
	if retained := os.Getenv("TEST_KEEP_GENERATED"); retained != "" {
		var err error
		root, err = os.MkdirTemp(retained, "jsoncodec-")
		require.NoError(t, err)
		t.Logf("retained generated fixture: %s", root)
	}
	mod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	mod = []byte(strings.Replace(string(mod), "module goa.design/goa-ai", "module codec.local", 1))
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), mod, 0o600)) // #nosec G703 -- root is t.TempDir, with a constant child name.
	sum, err := os.ReadFile("../../go.sum")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.sum"), sum, 0o600)) // #nosec G703 -- root is t.TempDir, with a constant child name.
	for _, file := range files {
		_, err := file.Render(root)
		require.NoError(t, err)
	}
	return root
}

// runGo bounds compiled-fixture checks independently of the outer test process.
func runGo(t *testing.T, root string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...) // #nosec G204 -- arguments are fixed test commands, never external input.
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	t.Logf("%s", output)
	require.NoError(t, err, "%s", output)
}
