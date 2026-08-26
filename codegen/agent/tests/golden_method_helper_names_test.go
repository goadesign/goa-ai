// These tests verify that every declaration in an agent-local toolset helper
// package receives one generation-time name shared by all generated files.
package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenMethodHelperNameCollisions(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MethodHelperNameCollisions())
	used := renderedFileContent(t, files, "gen/alpha/agents/scribe/helpers/used_tools.go")
	executor := renderedFileContent(t, files, "gen/alpha/agents/scribe/helpers/service_executor.go")

	require.Contains(t, used, "Client   tools.Ident")
	require.Contains(t, used, "ExecOpt2 tools.Ident")
	require.Contains(t, used, "type AgentIDPayload")
	require.Contains(t, used, "type AgentIDPayload2")
	require.Contains(t, used, "func NewAgentIDCall(")
	require.Contains(t, used, "func NewAgentIDCall2(")
	require.Contains(t, used, "type ExecOptPayload")
	require.Contains(t, used, "func NewExecOptCall(")
	require.Contains(t, executor, "type (\n\tseCfg struct")
	require.Contains(t, executor, "ExecOpt interface")
	require.Contains(t, executor, "func WithClient(")
	require.Contains(t, executor, "func WithClient2(")
	require.Contains(t, executor, "methodOut, err := cfg.execOptCaller(ctx, methodIn)")
	require.NotContains(t, executor, "map[tools.Ident]func(context.Context, any) (any, error)")

	assertGoldenGo(t, "method_helper_name_collisions", "used_tools.go.golden", used)
	assertGoldenGo(t, "method_helper_name_collisions", "service_executor.go.golden", executor)

	paths := make(map[string]int)
	for _, file := range files {
		paths[filepath.ToSlash(file.Path)]++
	}
	require.Equal(t, 1, paths["gen/alpha/agents/scribe/helpers/used_tools.go"])
	require.Equal(t, 1, paths["gen/alpha/agents/scribe/helpers/service_executor.go"])
}

func TestMethodHelperNormalPublicNamesStayStable(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelf())
	used := renderedFileContent(t, files, "gen/alpha/agents/scribe/lookup/used_tools.go")
	executor := renderedFileContent(t, files, "gen/alpha/agents/scribe/lookup/service_executor.go")

	require.Contains(t, used, "ByID tools.Ident")
	require.Contains(t, used, "type ByIDPayload")
	require.Contains(t, used, "type ByIDResult")
	require.Contains(t, used, "func NewByIDCall(")
	require.Contains(t, executor, "func WithByID(")
	require.Contains(t, executor, "func NewScribeLookupExec(")
}
