// Package codegen checks that MCP example stubs use the final service names chosen
// by the same Goa generation run.
package codegen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goagenerator "goa.design/goa/v3/codegen/generator"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestMCPExampleUsesFinalConstructorNames(t *testing.T) {
	goaAIDirectory := testModuleDirectory(t, "goa.design/goa-ai")
	goaDirectory := testModuleDirectory(t, "goa.design/goa/v3")
	t.Setenv("GOWORK", "off")
	previousRoot := expr.Root
	previousMCPRoot := mcpexpr.Root
	defer func() {
		expr.Root = previousRoot
		mcpexpr.Root = previousMCPRoot
		eval.Reset()
	}()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte(fmt.Sprintf(`module example.local

go 1.25

require (
	goa.design/goa-ai v0.0.0
	goa.design/goa/v3 v3.0.0
)

replace goa.design/goa-ai => %s

replace goa.design/goa/v3 => %s
`, filepath.ToSlash(goaAIDirectory), filepath.ToSlash(goaDirectory))),
		0o600,
	))

	configureCollidingExampleDesign(t)
	_, err := goagenerator.Generate(dir, "gen", false)
	require.NoError(t, err)
	configureCollidingExampleDesign(t)
	_, err = goagenerator.Generate(dir, "example", false)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./...")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

// configureCollidingExampleDesign registers two complete designs whose service
// names need different final Go constructors.
func configureCollidingExampleDesign(t *testing.T) {
	t.Helper()
	eval.Reset()
	mcpexpr.Root = mcpexpr.NewRoot()
	first, firstMethods := testService("read_value", "run")
	second, secondMethods := testService("read-value", "run")
	root := testRootExpr([]*expr.ServiceExpr{first, second}, []*expr.HTTPServiceExpr{
		jsonrpcService(first, "/first"),
		jsonrpcService(second, "/second"),
	})
	root.API.Name = "colliding_examples"
	root.API.Version = "1.0"
	root.API.GRPC = &expr.GRPCExpr{}
	root.API.RandomizerFactory = expr.NewDeterministicRandomizerFactory()
	// Let Goa create the default server used when a design omits Server.
	root.API.Servers = nil
	// The DSL links each transport service to the transport that contains it.
	for _, service := range root.API.HTTP.Services {
		service.Root = root.API.HTTP
	}
	for _, service := range root.API.JSONRPC.Services {
		service.Root = &root.API.JSONRPC.HTTPExpr
	}
	expr.Root = root
	require.NoError(t, eval.Register(root))
	require.NoError(t, eval.Register(mcpexpr.Root))
	mcpexpr.Root.RegisterMCP(first, &mcpexpr.MCPExpr{
		Name:    "first",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{{
			Name:        "run_first",
			Description: "Runs the first service.",
			Method:      firstMethods["run"],
		}},
	})
	mcpexpr.Root.RegisterMCP(second, &mcpexpr.MCPExpr{
		Name:    "second",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{{
			Name:        "run_second",
			Description: "Runs the second service.",
			Method:      secondMethods["run"],
		}},
	})
	require.NoError(t, eval.RunDSL())
}
