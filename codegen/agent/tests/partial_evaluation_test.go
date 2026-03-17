package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestConfigTemplateSpecializesMCPCallerValidation ensures config validation
// emits direct checks for known MCP bindings instead of rebuilding a static
// list at runtime.
func TestConfigTemplateSpecializesMCPCallerValidation(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MCPUse())
	config := fileContent(t, files, "gen/alpha/agents/scribe/config.go")

	require.NotContains(t, config, "required := []string")
	require.NotContains(t, config, "for _, id := range required")
	require.Contains(t, config, "if c.MCPCallers == nil {")
	require.Contains(t, config, "if c.MCPCallers[ScribeCalcServiceCoreSuiteToolsetID] == nil {")
	require.Contains(t, config, `return fmt.Errorf("mcp caller for %s is required", ScribeCalcServiceCoreSuiteToolsetID)`)
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
