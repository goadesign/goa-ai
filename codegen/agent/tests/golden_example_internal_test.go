package tests

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// buildAndGenerateExample provided by golden_helpers_test.go

func TestExampleInternal_MethodBacked(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.MethodComplexEmbedded())

	// Bootstrap
	boot := fileContent(t, files, "internal/agents/bootstrap/bootstrap.go")
	assertGoldenGo(t, "example_internal_method", "bootstrap.go.golden", boot)

	// Planner stub
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")
	assertGoldenGo(t, "example_internal_method", "planner.go.golden", plan)

	// Executor stub for toolset profiles
	exec := fileContent(t, files, "internal/agents/scribe/toolsets/profiles/execute.go")
	require.Contains(t, exec, "profilesspecs.UpsertPayloadCodec.FromJSON(call.Payload)")
	require.Contains(t, exec, "profilesspecs.SpecUpsert.Payload.ExampleJSON")
	assertGoldenGo(t, "example_internal_method", "executor.go.golden", exec)
}

func TestExampleInternal_MCP(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.MCPUse())

	// Bootstrap should include MCP caller stubs
	boot := fileContent(t, files, "internal/agents/bootstrap/bootstrap.go")
	assertGoldenGo(t, "example_internal_mcp", "bootstrap.go.golden", boot)

	// Planner stub exists
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")
	assertGoldenGo(t, "example_internal_mcp", "planner.go.golden", plan)
}

// TestExampleInternalUsesGeneratedToolNames checks that independently planned
// example files call the exact names written by goa gen.
func TestExampleInternalUsesGeneratedToolNames(t *testing.T) {
	design := exampleToolNameCollisionDesign()
	generated := buildAndGenerate(t, design)
	codecs := fileContent(t, generated, "gen/alpha/toolsets/ops/codecs.go")
	specs := fileContent(t, generated, "gen/alpha/toolsets/ops/specs.go")

	examples := buildAndGenerateExample(t, design)
	executor := renderedFileContent(t, examples, "internal/agents/worker/toolsets/ops/execute.go")

	codecRefs := regexp.MustCompile(`opsspecs\.([A-Za-z0-9]+PayloadCodec)\.FromJSON`).FindAllStringSubmatch(executor, -1)
	require.Len(t, codecRefs, 2)
	for _, match := range codecRefs {
		require.Contains(t, codecs, match[1]+" = tools.JSONCodec")
	}
	specRefs := regexp.MustCompile(`opsspecs\.(Spec[A-Za-z0-9]+)\.Payload\.ExampleJSON`).FindAllStringSubmatch(executor, -1)
	require.Len(t, specRefs, 2)
	for _, match := range specRefs {
		require.Contains(t, specs, match[1]+" = tools.ToolSpec")
	}
	for _, file := range examples {
		require.False(t, strings.HasPrefix(file.Path, "gen/"), file.Path)
	}
}

// exampleToolNameCollisionDesign defines two method-backed tools whose names
// become the same Go name before the package chooses unique spellings.
func exampleToolNameCollisionDesign() func() {
	return func() {
		API("example_names", func() {})
		Service("alpha", func() {
			Method("by_dash", func() {
				Payload(func() {
					Attribute("id", String, "Item ID")
					Required("id")
				})
				Result(String)
			})
			Method("by_underscore", func() {
				Payload(func() {
					Attribute("id", String, "Item ID")
					Required("id")
				})
				Result(String)
			})
			aidsl.Agent("worker", "Worker", func() {
				aidsl.Use("ops", func() {
					aidsl.Tool("by-id", "Find by ID", func() {
						aidsl.BindTo("by_dash")
					})
					aidsl.Tool("by_id", "Find by ID", func() {
						aidsl.BindTo("by_underscore")
					})
				})
			})
		})
	}
}

