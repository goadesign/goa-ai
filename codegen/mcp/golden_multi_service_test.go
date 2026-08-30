package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// TestMultiServiceGeneratesAdapterStubs verifies that each generated MCP
// service is backed by its matching user service in example servers.
func TestMultiServiceGeneratesAdapterStubs(t *testing.T) {
	// Two services with one method each
	alpha := &expr.ServiceExpr{Name: "Alpha", Methods: []*expr.MethodExpr{{Name: "One"}}}
	beta := &expr.ServiceExpr{Name: "Beta", Methods: []*expr.MethodExpr{{Name: "Two"}}}

	// Existing example stubs (to be replaced)
	alphaHeader := &codegen.SectionTemplate{Name: headerSection, Data: map[string]any{
		"Imports": []*codegen.ImportSpec{{Path: "example.com/assistant/gen/mcp_alpha", Name: "mcpalpha"}},
	}}
	alphaBody := &codegen.SectionTemplate{
		Name:   "body",
		Source: "func NewMcpAlpha() mcpalpha.Service { return &mcpAlphasrvc{} }",
	}
	alphaStub := &codegen.File{
		Path:             "mcp_alpha.go",
		SectionTemplates: []*codegen.SectionTemplate{alphaHeader, alphaBody},
	}

	betaHeader := &codegen.SectionTemplate{Name: headerSection, Data: map[string]any{
		"Imports": []*codegen.ImportSpec{{Path: "example.com/assistant/gen/mcp_beta", Name: "mcpbeta"}},
	}}
	betaBody := &codegen.SectionTemplate{
		Name:   "body",
		Source: "func NewMcpBeta() mcpbeta.Service { return &mcpBetasrvc{} }",
	}
	betaStub := &codegen.File{Path: "mcp_beta.go", SectionTemplates: []*codegen.SectionTemplate{betaHeader, betaBody}}

	files := []*codegen.File{alphaStub, betaStub}

	// Generate adapter stubs for both services and replace bodies
	_, err := generateExampleAdapterStubs([]exampleMCPService{
		{
			service:             alpha,
			mcpConstructorName:  "NewMcpAlpha",
			userConstructorName: "NewAlpha",
			mcpServiceInterface: "Service",
		},
		{
			service:             beta,
			mcpConstructorName:  "NewMcpBeta",
			userConstructorName: "NewBeta",
			mcpServiceInterface: "Service",
		},
	}, files)
	require.NoError(t, err)

	// Validate stubs were replaced with template section
	var alphaHasStub, betaHasStub bool
	for _, s := range alphaStub.SectionTemplates {
		if s.Name == exampleMCPStubSection && s.Source != "" {
			alphaHasStub = true
		}
	}
	for _, s := range betaStub.SectionTemplates {
		if s.Name == exampleMCPStubSection && s.Source != "" {
			betaHasStub = true
		}
	}
	require.True(t, alphaHasStub, "alpha stub not generated")
	require.True(t, betaHasStub, "beta stub not generated")
}
