package codegen

import (
    "testing"

    "github.com/stretchr/testify/require"
    "goa.design/goa/v3/codegen"
    "goa.design/goa/v3/expr"
)

// Ensures CLI patching and adapter stub generation work for multiple MCP-enabled services.
func TestMultiService_GeneratesCLIAndStubs(t *testing.T) {
    // Two services with one method each
    alpha := &expr.ServiceExpr{Name: "Alpha", Methods: []*expr.MethodExpr{{Name: "One"}}}
    beta := &expr.ServiceExpr{Name: "Beta", Methods: []*expr.MethodExpr{{Name: "Two"}}}

    // Server referencing both services
    svr := &expr.ServerExpr{Name: "orchestrator", Services: []string{"Alpha", "Beta"}}

    // CLI file for server
    cliHeader := &codegen.SectionTemplate{
        Name: headerSection,
        Data: map[string]any{
            "Imports": []*codegen.ImportSpec{
                {Path: "example.com/assistant/gen/jsonrpc/cli/orchestrator/cli"},
            },
        },
    }
    cliStart := &codegen.SectionTemplate{Name: "cli-http-start", Source: "// original"}
    cliFile := &codegen.File{
        Path: "cmd/orchestrator-cli/jsonrpc.go",
        SectionTemplates: []*codegen.SectionTemplate{cliHeader, cliStart},
    }

    // Existing example stubs (to be replaced)
    alphaHeader := &codegen.SectionTemplate{Name: headerSection, Data: map[string]any{
        "Imports": []*codegen.ImportSpec{{Path: "example.com/assistant/gen/mcp_alpha", Name: "mcpalpha"}},
    }}
    alphaBody := &codegen.SectionTemplate{
        Name:   "body",
        Source: "func NewMcpAlpha() mcpalpha.Service { return &mcpAlphasrvc{} }",
    }
    alphaStub := &codegen.File{
        Path:               "mcp_alpha.go",
        SectionTemplates:   []*codegen.SectionTemplate{alphaHeader, alphaBody},
    }

    betaHeader := &codegen.SectionTemplate{Name: headerSection, Data: map[string]any{
        "Imports": []*codegen.ImportSpec{{Path: "example.com/assistant/gen/mcp_beta", Name: "mcpbeta"}},
    }}
    betaBody := &codegen.SectionTemplate{
        Name:   "body",
        Source: "func NewMcpBeta() mcpbeta.Service { return &mcpBetasrvc{} }",
    }
    betaStub := &codegen.File{Path: "mcp_beta.go", SectionTemplates: []*codegen.SectionTemplate{betaHeader, betaBody}}

    files := []*codegen.File{cliFile, alphaStub, betaStub}

    // Patch CLI to use adapter clients for both services
    files = patchCLIForServer("orchestrator", svr, []*expr.ServiceExpr{alpha, beta}, files)

    // Generate adapter stubs for both services and replace bodies
    generateExampleAdapterStubs([]*expr.ServiceExpr{alpha, beta}, files)

    // Validate CLI header contains both adapter client imports
    var importPaths []string
    if data, ok := cliHeader.Data.(map[string]any); ok {
        if imv, ok2 := data["Imports"]; ok2 {
            if specs, ok3 := imv.([]*codegen.ImportSpec); ok3 {
                for _, s := range specs {
                    importPaths = append(importPaths, s.Path)
                }
            }
        }
    }
    require.Contains(t, importPaths, "example.com/assistant/gen/mcp_alpha/adapter/client")
    require.Contains(t, importPaths, "example.com/assistant/gen/mcp_beta/adapter/client")
    require.NotEqual(t, "// original", cliStart.Source)

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
