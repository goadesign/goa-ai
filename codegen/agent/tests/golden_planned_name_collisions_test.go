// Package tests verifies that generated declarations and their references use
// the names selected before source files are rendered.
package tests

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

func TestGoldenToolSpecNameCollisions(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecNameCollisions())
	codecs := renderedFileContent(t, files, "gen/alpha/toolsets/helpers/codecs.go")
	specs := renderedFileContent(t, files, "gen/alpha/toolsets/helpers/specs.go")

	require.Contains(t, codecs, "var inspectPayloadFields2 =")
	require.Contains(t, codecs, "func validateInspectPayloadJSON2(")
	require.Contains(t, codecs, "func enrichInspectPayloadValidationError2(")
	require.Contains(t, codecs, "func invalidInspectPayloadFieldTypeError2(")
	require.Contains(t, specs, "tools.CloneFieldMetadata(inspectPayloadFields2)")

	assertGoldenGo(t, "tool_spec_name_collisions", "codecs.go.golden", codecs)
	assertGoldenGo(t, "tool_spec_name_collisions", "specs.go.golden", specs)
	complete := buildCompleteGeneratedFiles(t, testscenarios.ToolSpecNameCollisions())
	root := writeCompleteGeneratedModule(t, complete)
	for _, pkg := range []string{
		"field_descriptions",
		"field_json_types",
		"field_type_error",
		"json_validator",
		"validation_description",
	} {
		dir := filepath.Join(root, "custom", pkg)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "value.go"),
			[]byte("package "+pkg+"\n\ntype Value string\n"),
			0o600,
		))
	}
	runGeneratedPackageTest(t, root, "./gen/alpha/toolsets/helpers/...")
}

func TestGoldenCompletionNameCollisions(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.CompletionNameCollisions())
	codecs := renderedFileContent(t, files, "gen/tasks/completions/codecs.go")
	specs := renderedFileContent(t, files, "gen/tasks/completions/specs.go")
	exampleFiles := buildAndGenerateExample(t, testscenarios.CompletionNameCollisions())
	main := renderedFileContent(t, exampleFiles, "cmd/tasks/main.go")

	require.Contains(t, specs, "func DraftExample2() rawjson.Message")
	require.Contains(t, specs, "func SpecCompleteDraft() completion.Spec")
	require.Contains(t, specs, "func SpecDraft2() completion.Spec")
	require.Contains(t, specs, "func SpecDraftExample() completion.Spec")
	require.Contains(t, specs, "func SpecSpecDraft() completion.Spec")
	require.Contains(t, specs, "func SpecStreamCompleteDraft() completion.Spec")
	require.Contains(t, specs, "func CompleteDraft2(")
	require.Contains(t, specs, "func StreamCompleteDraft2(")
	require.Contains(t, main, "completions.DraftExample2()")
	require.Contains(t, main, "completions.CompleteDraft2(")
	require.Contains(t, main, "completions.StreamCompleteDraft2(")

	assertGoldenGo(t, "completion_name_collisions", "codecs.go.golden", codecs)
	assertGoldenGo(t, "completion_name_collisions", "specs.go.golden", specs)
	assertGoldenGo(t, "completion_name_collisions", "main.go.golden", main)
	runCompleteGeneratedPackageTest(t, files, "./gen/tasks/completions/...")
}

func TestGoldenMCPBootstrapNameCollisions(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.MCPBootstrapNameCollisions())
	bootstrap := renderedFileContent(t, files, "internal/agents/alpha/bootstrap/bootstrap.go")

	require.Contains(t, bootstrap, "mcpCalcAPICoreEndpoint  =")
	require.Contains(t, bootstrap, "mcpCalcAPICoreEndpoint2 =")
	assertGoldenGo(t, "mcp_bootstrap_name_collisions", "bootstrap.go.golden", bootstrap)
}

func TestGoldenBootstrapImportNameCollisions(t *testing.T) {
	generated := buildCompleteGeneratedFiles(t, testscenarios.BootstrapImportNameCollisions())
	examples := testhelpers.BuildAndGenerateWithExamplePkg(
		t,
		"generated.local/gen",
		testscenarios.BootstrapImportNameCollisions(),
	)
	files := append(slices.Clone(generated), examples...)
	bootstrap := renderedFileContent(t, files, "internal/agents/alpha/bootstrap/bootstrap.go")

	require.Contains(t, bootstrap, `context2 "generated.local/gen/alpha/agents/context"`)
	require.Contains(t, bootstrap, "func New(ctx context.Context, store storage.Store)")
	require.Contains(t, bootstrap, "cfg := context2.ContextAgentConfig")
	assertGoldenGo(t, "bootstrap_import_name_collisions", "bootstrap.go.golden", bootstrap)
	runCompleteGeneratedPackageTest(t, files, "./internal/agents/alpha/bootstrap")
}

func TestGoldenExampleMainImportNameCollisions(t *testing.T) {
	design := testscenarios.ExampleMainImportNameCollisions()
	generated := buildCompleteGeneratedFiles(t, design)
	examples := testhelpers.BuildAndGenerateWithExamplePkg(t, "generated.local/gen", design)
	files := append(slices.Clone(generated), examples...)
	main := renderedFileContent(t, files, "cmd/alpha/main.go")

	for _, imported := range []string{
		`bootstrap "generated.local/gen/alpha/agents/bootstrap"`,
		`completions "generated.local/gen/alpha/agents/completions"`,
		`context2 "generated.local/gen/alpha/agents/context"`,
		`fmt2 "generated.local/gen/alpha/agents/fmt"`,
		`io "generated.local/gen/alpha/agents/io"`,
		`log "generated.local/gen/alpha/agents/log"`,
		`model "generated.local/gen/alpha/agents/model"`,
		`rawjson "generated.local/gen/alpha/agents/rawjson"`,
		`runtime "generated.local/gen/alpha/agents/runtime"`,
		`storageinmem "generated.local/gen/alpha/agents/storageinmem"`,
		`time "generated.local/gen/alpha/agents/time"`,
		`bootstrap2 "generated.local/internal/agents/alpha/bootstrap"`,
		`completions2 "generated.local/gen/alpha/completions"`,
		`context "context"`,
		`fmt "fmt"`,
		`io2 "io"`,
		`log2 "log"`,
		`model2 "goa.design/goa-ai/runtime/agent/model"`,
		`rawjson2 "goa.design/goa-ai/runtime/agent/rawjson"`,
		`runtime2 "goa.design/goa-ai/runtime/agent/runtime"`,
		`storageinmem2 "goa.design/goa-ai/runtime/agent/storage/inmem"`,
		`time2 "time"`,
	} {
		require.Contains(t, main, imported)
	}
	assertGoldenGo(t, "example_main_import_name_collisions", "main.go.golden", main)
	runCompleteGeneratedPackageTest(t, files, "./cmd/alpha")
}
