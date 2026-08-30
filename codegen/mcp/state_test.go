package codegen

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestMCPPluginDoesNotReusePreviousPrepareRun(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	firstRoots := testMCPRunRoots("alpha")
	first := new(mcpPlugin)
	require.NoError(t, first.prepare("", firstRoots))
	require.Len(t, first.prepared, 1)
	require.Equal(t, "alpha", first.prepared[0].userService.Name)

	secondRoots := testMCPRunRoots("beta")
	second := new(mcpPlugin)
	require.NoError(t, second.prepare("", secondRoots))
	require.Len(t, second.prepared, 1)
	require.Equal(t, "beta", second.prepared[0].userService.Name)
}

// TestMCPPluginUsesPreparedRoot sets a conflicting package root and checks that
// Prepare uses the MCP root supplied for this run.
func TestMCPPluginUsesPreparedRoot(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	roots := testMCPRunRoots("selected")
	mcpexpr.Root = mcpexpr.NewRoot()
	conflictService, conflictMethods := testService("conflict", "list")
	mcpexpr.Root.RegisterMCP(conflictService, testMCPExpr("conflict", conflictMethods["list"]))

	plugin := new(mcpPlugin)
	require.NoError(t, plugin.prepare("", roots))
	require.Len(t, plugin.prepared, 1)
	require.Equal(t, "selected", plugin.prepared[0].mcp.Name)
}

// TestMCPPluginRequiresOnePreparedRoot checks that a run cannot omit or
// provide two MCP roots.
func TestMCPPluginRequiresOnePreparedRoot(t *testing.T) {
	goaRoot := testRootExpr(nil, nil)
	tests := map[string]struct {
		roots []eval.Root
		error string
	}{
		"missing": {
			roots: []eval.Root{goaRoot},
			error: "do not contain an MCP root",
		},
		"duplicate": {
			roots: []eval.Root{goaRoot, mcpexpr.NewRoot(), mcpexpr.NewRoot()},
			error: "more than one MCP root",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plugin := new(mcpPlugin)
			err := plugin.prepare("", test.roots)
			require.ErrorContains(t, err, test.error)
		})
	}
}

// TestMCPPluginPreparesIndependentRootsAtTheSameTime checks that concurrent
// runs read only their own MCP configuration.
func TestMCPPluginPreparesIndependentRootsAtTheSameTime(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	const runCount = 8
	runs := make([][]eval.Root, runCount)
	mcpexpr.Root = mcpexpr.NewRoot()
	for index := range runs {
		name := fmt.Sprintf("service_%d", index)
		runs[index] = testMCPRunRoots(name)
		goaRoot := runs[index][0].(*expr.RootExpr)
		service := goaRoot.Services[0]
		mcpexpr.Root.RegisterMCP(service, testMCPExpr("conflict_"+name, service.Methods[0]))
	}

	start := make(chan struct{})
	errors := make(chan error, runCount)
	prepared := make([][]*preparedMCPService, runCount)
	var wait sync.WaitGroup
	for index := range runs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			plugin := new(mcpPlugin)
			if err := plugin.prepare("", runs[index]); err != nil {
				errors <- err
				return
			}
			prepared[index] = plugin.prepared
			errors <- nil
		}(index)
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	for index, services := range prepared {
		require.Len(t, services, 1)
		require.Equal(t, fmt.Sprintf("service_%d", index), services[0].mcp.Name)
	}
}

func TestPrepareServices_OrderIsStableAcrossRuns(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	baseline, err := prepareMCPServices(testStableGenerationRoots())
	require.NoError(t, err)
	baselineNames := preparedServiceNames(baseline)

	for range 12 {
		services, runErr := prepareMCPServices(testStableGenerationRoots())
		require.NoError(t, runErr)
		require.Equal(t, baselineNames, preparedServiceNames(services))
	}
}

func testStableGenerationRoots() []eval.Root {
	serviceNames := []string{"beta", "alpha", "gamma", "delta"}
	services := make([]*expr.ServiceExpr, 0, len(serviceNames))
	jsonrpcServices := make([]*expr.HTTPServiceExpr, 0, len(serviceNames))
	mcpRoot := mcpexpr.NewRoot()

	for _, name := range serviceNames {
		svc, methods := testService(name, "list")
		services = append(services, svc)
		jsonrpcServices = append(jsonrpcServices, jsonrpcService(svc, "/"+name+"-rpc"))
		mcpRoot.RegisterMCP(svc, &mcpexpr.MCPExpr{
			Name:    name,
			Version: "1.0.0",
			Tools: []*mcpexpr.ToolExpr{
				{Name: "list", Method: methods["list"]},
			},
		})
	}

	return []eval.Root{testRootExpr(services, jsonrpcServices), mcpRoot}
}

// testMCPRunRoots returns one Goa root paired with its exact MCP root.
func testMCPRunRoots(name string) []eval.Root {
	service, methods := testService(name, "list")
	goaRoot := testRootExpr(
		[]*expr.ServiceExpr{service},
		[]*expr.HTTPServiceExpr{jsonrpcService(service, "/"+name+"-rpc")},
	)
	mcpRoot := mcpexpr.NewRoot()
	mcpRoot.RegisterMCP(service, testMCPExpr(name, methods["list"]))
	return []eval.Root{goaRoot, mcpRoot}
}

// testMCPExpr returns one MCP server with a single tool method.
func testMCPExpr(name string, method *expr.MethodExpr) *mcpexpr.MCPExpr {
	return &mcpexpr.MCPExpr{
		Name:    name,
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "list", Method: method},
		},
	}
}

func preparedServiceNames(services []*preparedMCPService) []string {
	names := make([]string, len(services))
	for index, service := range services {
		names[index] = service.mcpService.Name
	}
	return names
}
