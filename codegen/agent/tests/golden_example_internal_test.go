package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

// buildAndGenerateExample provided by golden_helpers_test.go

func TestExampleInternal_MethodBacked(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.MethodComplexEmbedded())

	// Bootstrap
	boot := fileContent(t, files, "internal/agents/bootstrap/bootstrap.go")
	assertGoldenGo(t, "example_internal_method", "bootstrap.go.golden", boot)

	// Planner stub
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")
	require.NotContains(t, plan, "decode generated")
	require.Contains(t, plan, "Hello from example planner.")
	assertGoldenGo(t, "example_internal_method", "planner.go.golden", plan)

	// Executor stub for toolset profiles
	exec := fileContent(t, files, "internal/agents/scribe/toolsets/profiles/execute.go")
	require.NotContains(t, exec, `rawjson.Message("")`)
	require.Contains(t, exec, "generated executor requires an application implementation")
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

func TestExampleInternal_NoResultToolReturnsEmptySuccess(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.NoResultMethod())
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")
	exec := fileContent(t, files, "internal/agents/scribe/toolsets/ops/execute.go")

	require.Contains(t, plan, "build generated ops.purge call")
	require.Contains(t, plan, "Tool %s completed successfully")
	require.Contains(t, exec, "return runtime.Executed(&planner.ToolResult{Name: call.Name}), nil")
	require.NotContains(t, exec, "generated executor requires an application implementation")
	require.NotContains(t, exec, `"fmt"`)
}

func TestExampleInternal_InjectedToolUsesComposedDecoder(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.InjectLabelExample())
	exec := fileContent(t, files, "internal/agents/scribe/toolsets/helpers/execute.go")

	require.Contains(
		t,
		exec,
		"helpersspecs.DecodeLookupHousehold(call.Payload, *meta, meta.Labels)",
	)
	require.NotContains(t, exec, "SpecLookupHousehold().Payload.Codec.FromJSON")
}

func TestExampleInternal_BoundedToolIsNotSelectedWithoutExecutorBounds(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.ServiceToolsetBindSelfBoundedResult())
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")

	require.Contains(t, plan, "Hello from example planner.")
	require.NotContains(t, plan, "build generated search call")
}

func TestExampleInternal_CompletionWithoutExampleIsNotExecuted(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.ServiceCompletionWithoutExampleWithAgent())
	main := fileContent(t, files, "cmd/tasks/main.go")

	require.NotContains(t, main, "newExampleCompletionClient")
	require.NotContains(t, main, "DraftFromTranscriptExample")
}

func TestGeneratedProviderUsesExactMethodCallArity(t *testing.T) {
	noResultFiles := testhelpers.BuildAndGenerateWithPkg(
		t,
		"generated.local/gen",
		testscenarios.NoResultMethod(),
	)
	noResultProvider := generatedContentBySuffix(t, noResultFiles, "alpha/toolsets/ops/provider.go")
	require.Contains(t, noResultProvider, "err = p.svc.Purge(ctx, methodIn)")
	require.Contains(t, noResultProvider, "err = p.svc.Heartbeat(ctx)")
	require.NotContains(t, noResultProvider, "methodOut")

	resultFiles := testhelpers.BuildAndGenerateWithPkg(
		t,
		"generated.local/gen",
		testscenarios.EmptyPayloadResultMethod(),
	)
	resultProvider := generatedContentBySuffix(t, resultFiles, "alpha/toolsets/ops/provider.go")

	require.Contains(t, resultProvider, "methodOut, err := p.svc.Status(ctx)")
	require.NotContains(t, resultProvider, "InitStatusMethodPayload")
}
