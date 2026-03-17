package codegen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	gcodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestGenerate_DoesNotReusePreviousPrepareRun(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	firstService, firstMethods := testService("alpha", "list")
	firstRoot := testRootExpr(
		[]*expr.ServiceExpr{firstService},
		[]*expr.HTTPServiceExpr{jsonrpcService(firstService, "/alpha-rpc")},
	)
	mcpexpr.Root.RegisterMCP(firstService, &mcpexpr.MCPExpr{
		Name:    "alpha",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "list", Method: firstMethods["list"]},
		},
	})
	require.NoError(t, PrepareServices("", []eval.Root{firstRoot}))
	_, err := Generate("example.com/first/gen", []eval.Root{firstRoot}, nil)
	require.NoError(t, err)

	secondService, secondMethods := testService("beta", "status")
	secondRoot := testRootExpr(
		[]*expr.ServiceExpr{secondService},
		[]*expr.HTTPServiceExpr{jsonrpcService(secondService, "/beta-rpc")},
	)
	mcpexpr.Root = mcpexpr.NewRoot()
	mcpexpr.Root.RegisterMCP(secondService, &mcpexpr.MCPExpr{
		Name:    "beta",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "status", Method: secondMethods["status"]},
		},
	})

	files, err := Generate("example.com/second/gen", []eval.Root{secondRoot}, nil)

	require.NoError(t, err)
	require.True(t, hasGeneratedFile(files, filepath.Join(gcodegen.Gendir, "mcp_beta", "adapter_server.go")))
	require.False(t, hasPathFragment(files, "mcp_alpha"))
}

func TestGenerate_FileOrderIsStableAcrossRuns(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	root := testStableGenerationRoot()
	require.NoError(t, PrepareServices("", []eval.Root{root}))

	baseline, err := Generate("example.com/stable/gen", []eval.Root{root}, nil)
	require.NoError(t, err)
	baselinePaths := generatedPaths(baseline)

	for range 12 {
		files, runErr := Generate("example.com/stable/gen", []eval.Root{root}, nil)
		require.NoError(t, runErr)
		require.Equal(t, baselinePaths, generatedPaths(files))
	}
}

func testStableGenerationRoot() *expr.RootExpr {
	serviceNames := []string{"beta", "alpha", "gamma", "delta"}
	services := make([]*expr.ServiceExpr, 0, len(serviceNames))
	jsonrpcServices := make([]*expr.HTTPServiceExpr, 0, len(serviceNames))
	mcpexpr.Root = mcpexpr.NewRoot()

	for _, name := range serviceNames {
		svc, methods := testService(name, "list")
		services = append(services, svc)
		jsonrpcServices = append(jsonrpcServices, jsonrpcService(svc, "/"+name+"-rpc"))
		mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
			Name:    name,
			Version: "1.0.0",
			Tools: []*mcpexpr.ToolExpr{
				{Name: "list", Method: methods["list"]},
			},
		})
	}

	return testRootExpr(services, jsonrpcServices)
}

func hasGeneratedFile(files []*gcodegen.File, want string) bool {
	normWant := filepath.ToSlash(want)
	for _, file := range files {
		if filepath.ToSlash(file.Path) == normWant {
			return true
		}
	}
	return false
}

func hasPathFragment(files []*gcodegen.File, fragment string) bool {
	for _, file := range files {
		if strings.Contains(filepath.ToSlash(file.Path), fragment) {
			return true
		}
	}
	return false
}

func generatedPaths(files []*gcodegen.File) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, filepath.ToSlash(file.Path))
	}
	return paths
}
