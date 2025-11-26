package codegen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

func TestPatchCLIForServer_UsesMCPAdapterClient(t *testing.T) {
	// Arrange: CLI file with header and start sections
	header := &codegen.SectionTemplate{
		Name: headerSection,
		Data: map[string]any{
			"Imports": []*codegen.ImportSpec{
				{Path: "example.com/assistant/gen/jsonrpc/cli/orchestrator/cli"},
			},
		},
	}
	start := &codegen.SectionTemplate{Name: "cli-http-start", Source: "// original"}
	cliFile := &codegen.File{
		Path:             "cmd/orchestrator-cli/jsonrpc.go",
		SectionTemplates: []*codegen.SectionTemplate{header, start},
	}

	// One MCP-enabled service with one method
	svc := &expr.ServiceExpr{Name: "orchestrator", Methods: []*expr.MethodExpr{{Name: "EventsStream"}}}
	svr := &expr.ServerExpr{Name: "srv", Services: []string{"orchestrator"}}

	files := patchCLIForServer("orchestrator", svr, []*expr.ServiceExpr{svc}, []*codegen.File{cliFile})
	require.Len(t, files, 1)

	// Assert: header now imports the MCP adapter client and start section is replaced
	var hasAdapterImport bool
	if data, ok := header.Data.(map[string]any); ok {
		if imv, ok2 := data["Imports"]; ok2 {
			if specs, ok3 := imv.([]*codegen.ImportSpec); ok3 {
				for _, s := range specs {
					if s.Path == "example.com/assistant/gen/mcp_orchestrator/adapter/client" {
						hasAdapterImport = true
						break
					}
				}
			}
		}
	}
	require.True(t, hasAdapterImport, "expected adapter client import to be added")

	// The start section should have been rewritten by the template renderer
	require.NotEqual(t, "// original", start.Source)
}

func TestGenerateExampleAdapterStubs_ReplacesStub(t *testing.T) {
	// Arrange: existing stub file with a header and a dummy body
	svc := &expr.ServiceExpr{Name: "Orchestrator"}
	header := &codegen.SectionTemplate{
		Name: headerSection,
		Data: map[string]any{
			"Imports": []*codegen.ImportSpec{
				{Path: "example.com/assistant/gen/mcp_orchestrator", Name: "mcporchestrator"},
			},
		},
	}
	body := &codegen.SectionTemplate{
		Name:   "body",
		Source: "func NewMcpOrchestrator() mcporchestrator.Service { return &mcpOrchestratorsrvc{} }",
	}
	stub := &codegen.File{Path: "mcp_orchestrator.go", SectionTemplates: []*codegen.SectionTemplate{header, body}}

	files := generateExampleAdapterStubs([]*expr.ServiceExpr{svc}, []*codegen.File{stub})
	require.Len(t, files, 1)
	// Body should now contain a call to NewMCPAdapter(NewOrchestrator())
	found := false
	for _, s := range files[0].SectionTemplates {
		if s.Name == "example-mcp-stub" && strings.Contains(s.Source, "NewMCPAdapter(NewOrchestrator()") {
			found = true
		}
	}
	require.True(t, found, "expected example adapter stub to be generated")
}
