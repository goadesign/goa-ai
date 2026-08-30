package codegen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

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

	files, err := generateExampleAdapterStubs(
		[]exampleMCPService{{
			service:             svc,
			mcpConstructorName:  "NewMCPOrchestratorEndpoint",
			userConstructorName: "NewOrchestratorEndpoint",
			mcpServiceInterface: "ServiceEndpoint",
		}},
		[]*codegen.File{stub},
	)
	require.NoError(t, err)
	require.Len(t, files, 1)
	// The body uses Goa's final constructor and service interface names.
	found := false
	for _, s := range files[0].SectionTemplates {
		if s.Name == "example-mcp-stub" &&
			strings.Contains(s.Source, "func NewMCPOrchestratorEndpoint() mcporchestrator.ServiceEndpoint") &&
			strings.Contains(s.Source, "NewMCPAdapter(NewOrchestratorEndpoint()") {
			found = true
		}
	}
	require.True(t, found, "expected example adapter stub to be generated")
}

func TestGenerateExampleAdapterStubs_RequiresExpectedStubPath(t *testing.T) {
	svc := &expr.ServiceExpr{Name: "Orchestrator"}
	header := &codegen.SectionTemplate{
		Name: headerSection,
		Data: map[string]any{
			"Imports": []*codegen.ImportSpec{
				{Path: "example.com/assistant/gen/mcp_orchestrator", Name: "mcporchestrator"},
			},
		},
	}
	stub := &codegen.File{
		Path:             "unexpected.go",
		SectionTemplates: []*codegen.SectionTemplate{header},
	}

	_, err := generateExampleAdapterStubs(
		[]exampleMCPService{{service: svc}},
		[]*codegen.File{stub},
	)

	require.Error(t, err)
	require.ErrorContains(t, err, `expected MCP example stub "mcp_orchestrator.go"`)
}

func TestGenerateExampleAdapterStubs_RequiresExplicitMCPImportAlias(t *testing.T) {
	svc := &expr.ServiceExpr{Name: "Orchestrator"}
	header := &codegen.SectionTemplate{
		Name: headerSection,
		Data: map[string]any{
			"Imports": []*codegen.ImportSpec{
				{Path: "example.com/assistant/gen/mcp_orchestrator"},
			},
		},
	}
	stub := &codegen.File{
		Path:             "mcp_orchestrator.go",
		SectionTemplates: []*codegen.SectionTemplate{header},
	}

	_, err := generateExampleAdapterStubs(
		[]exampleMCPService{{service: svc}},
		[]*codegen.File{stub},
	)

	require.Error(t, err)
	require.ErrorContains(t, err, `must import "example.com/assistant/gen/mcp_orchestrator" with an explicit alias`)
}

func TestReplaceHTTPServiceByNameReplacesOriginalExactlyOnce(t *testing.T) {
	original := &expr.HTTPServiceExpr{ServiceExpr: &expr.ServiceExpr{Name: "orchestrator"}}
	replacement := &expr.HTTPServiceExpr{ServiceExpr: &expr.ServiceExpr{Name: "mcp_orchestrator"}}

	services := replaceHTTPServiceByName(
		[]*expr.HTTPServiceExpr{original, replacement, original},
		"orchestrator",
		replacement,
	)

	require.Len(t, services, 1)
	require.Same(t, replacement, services[0])
}

func TestRemoveMCPClientCommands(t *testing.T) {
	service := &expr.ServiceExpr{Name: "assistant"}
	root := &expr.RootExpr{API: &expr.APIExpr{Servers: []*expr.ServerExpr{{
		Name:     "orchestrator",
		Services: []string{"assistant", "mcp_assistant"},
	}}}}
	server := &codegen.File{Path: "cmd/orchestrator/main.go"}
	command := &codegen.File{Path: "cmd/orchestrator-cli/main.go"}
	parser := &codegen.File{Path: "gen/jsonrpc/cli/orchestrator/cli.go"}

	files := removeMCPClientCommands(
		root,
		[]exampleMCPService{{service: service}},
		[]*codegen.File{server, command, parser},
	)

	require.Equal(t, []*codegen.File{server}, files)
}

func TestRemoveMCPGeneratedClientCommands(t *testing.T) {
	root := &expr.RootExpr{API: &expr.APIExpr{Servers: []*expr.ServerExpr{{
		Name:     "orchestrator",
		Services: []string{"mcp_assistant"},
	}}}}
	planned := &plannedMCPService{
		prepared: &preparedMCPService{
			root:       root,
			mcpService: &expr.ServiceExpr{Name: "mcp_assistant"},
		},
		adapterData: &AdapterData{mcpPathName: "mcp_assistant"},
	}
	server := &codegen.File{Path: "gen/jsonrpc/mcp_assistant/server/server.go"}
	clientCLI := &codegen.File{Path: "gen/jsonrpc/mcp_assistant/client/cli.go"}
	serverCLI := &codegen.File{Path: "gen/jsonrpc/cli/orchestrator/cli.go"}

	files := removeMCPGeneratedClientCommands(
		[]*plannedMCPService{planned},
		[]*codegen.File{server, clientCLI, serverCLI},
	)

	require.Equal(t, []*codegen.File{server}, files)
}
