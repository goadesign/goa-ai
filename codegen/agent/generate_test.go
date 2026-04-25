package codegen

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentsExpr "goa.design/goa-ai/expr/agent"
)

func TestMCPExecutorFiles_DeduplicatesSameOriginToolsets(t *testing.T) {
	provider := &agentsExpr.ToolsetExpr{
		Name: "calc-remote",
		Provider: &agentsExpr.ProviderExpr{
			Kind:       agentsExpr.ProviderMCP,
			MCPService: "calc",
			MCPToolset: "core",
		},
	}
	used := &ToolsetData{
		Expr: &agentsExpr.ToolsetExpr{
			Name:     "calc-remote",
			Origin:   provider,
			Provider: provider.Provider,
		},
		QualifiedName:    "calc-remote",
		PathName:         "calc_remote",
		PackageName:      "calc_remote",
		Dir:              filepath.Join("gen", "alpha", "agents", "scribe", "calc_remote"),
		SpecsImportPath:  "example.com/gen/calc/toolsets/calc_remote",
		SpecsPackageName: "calc_remote_specs",
	}
	exported := &ToolsetData{
		Expr: &agentsExpr.ToolsetExpr{
			Name:     "calc-remote",
			Origin:   provider,
			Provider: provider.Provider,
		},
		QualifiedName:    used.QualifiedName,
		PathName:         used.PathName,
		PackageName:      used.PackageName,
		Dir:              used.Dir,
		SpecsImportPath:  used.SpecsImportPath,
		SpecsPackageName: used.SpecsPackageName,
	}
	agent := &AgentData{
		GoName:      "Scribe",
		AllToolsets: []*ToolsetData{used, exported},
	}

	files := mcpExecutorFiles(agent)

	require.Len(t, files, 1)
	require.Equal(t, filepath.Join(used.Dir, "mcp_executor.go"), files[0].Path)
}

func TestExecutorTemplatesUseSpecializedDispatch(t *testing.T) {
	mcp := agentsTemplates.Read(mcpExecutorFileT)
	assert.Contains(t, mcp, "switch call.Name")
	assert.Contains(t, mcp, "Payload: json.RawMessage(call.Payload)")
	assert.NotContains(t, mcp, "PayloadCodec(full)")
	assert.NotContains(t, mcp, "strings.HasPrefix")

	service := agentsTemplates.Read(serviceExecutorFileT)
	assert.Contains(t, service, "{{ $.Toolset.SpecsPackageName }}.{{ .ConstName }}PayloadCodec.FromJSON(call.Payload)")
	assert.NotContains(t, service, "PayloadCodec(string(call.Name))")
	assert.NotContains(t, service, "bounds = init")
}

func TestRegistryTemplateValidatesExecutorsBeforeRegistration(t *testing.T) {
	registry := agentsTemplates.Read(registryFileT)
	assert.Contains(t, registry, `return fmt.Errorf("missing executors for toolsets: %v", missing)`)
	assert.NotContains(t, registry, "no executor registered for toolset")
}
