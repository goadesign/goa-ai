package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// MCP bindings use explicit executable registration; agent configuration owns its planner.
func TestMCPRegistrationUsesExplicitExecutor(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MCPUse())
	config := fileContent(t, files, "gen/alpha/agents/scribe/config.go")
	registry := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	require.NotContains(t, config, "MCPCallers")
	require.Contains(t, registry, "WithCoreExecutor(")
	require.Contains(t, registry, "ActivityRetryPolicy:")
	require.Contains(t, registry, "MaxAttempts: 1")
	require.NotContains(t, registry, "NewMCPExecutor(")
}

// TestAgentToolsTemplateOmitsHintMapsWhenAbsent ensures exported toolset helpers
// do not emit empty hint-template map scaffolding when the DSL defines no
// hints.
func TestAgentToolsTemplateOmitsHintMapsWhenAbsent(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ExportsSimple())
	helpers := fileContent(t, files, "gen/alpha/agents/scribe/agenttools/search/helpers.go")

	require.NotContains(t, helpers, "var callRaw map[tools.Ident]string")
	require.NotContains(t, helpers, "var resultRaw map[tools.Ident]string")
	require.NotContains(t, helpers, "hints.CompileHintTemplates(")
}

// TestAgentToolsTemplateSpecializesHintMaps ensures exported toolset helpers
// emit direct literal maps when hint templates are statically known.
func TestAgentToolsTemplateSpecializesHintMaps(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ExportsWithHints())
	helpers := fileContent(t, files, "gen/alpha/agents/scribe/agenttools/search/helpers.go")

	require.NotContains(t, helpers, "var callRaw map[tools.Ident]string")
	require.NotContains(t, helpers, "var resultRaw map[tools.Ident]string")
	require.NotContains(t, helpers, "if callRaw == nil")
	require.NotContains(t, helpers, "if resultRaw == nil")
	require.Contains(t, helpers, "hints.CompileHintTemplates(map[tools.Ident]string{")
	require.Contains(t, helpers, `Find: "Searching for {{ .Query }}"`)
	require.Contains(t, helpers, `Find: "Found {{ .Result.Count }}"`)
}

// TestRegistryTemplateOmitsHintMapsWhenAbsent ensures used toolset
// registrations do not emit empty hint-template map scaffolding when the DSL
// defines no hints.
func TestRegistryTemplateOmitsHintMapsWhenAbsent(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelf())
	registry := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")

	require.Contains(t, registry, "agentsruntime.ToolCallMetaFromCall(*call)")
	require.NotContains(t, registry, "var callRaw map[tools.Ident]string")
	require.NotContains(t, registry, "var resultRaw map[tools.Ident]string")
	require.NotContains(t, registry, "hints.CompileHintTemplates(")
}

// TestRegistryTemplateSpecializesHintMaps ensures used toolset registrations
// emit direct literal maps when hint templates are statically known.
func TestRegistryTemplateSpecializesHintMaps(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelfHints())
	registry := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")

	require.NotContains(t, registry, "var callRaw map[tools.Ident]string")
	require.NotContains(t, registry, "var resultRaw map[tools.Ident]string")
	require.NotContains(t, registry, "if callRaw == nil")
	require.NotContains(t, registry, "if resultRaw == nil")
	require.Contains(t, registry, "hints.CompileHintTemplates(map[tools.Ident]string{")
	require.Contains(t, registry, `tools.Ident("lookup.by_id"): "Lookup {{ .ID }}"`)
	require.Contains(t, registry, `tools.Ident("lookup.by_id"): "Done {{ .Result.Ok }}"`)
}
