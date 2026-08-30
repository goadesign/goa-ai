// This file verifies that MCP tool registration plans every package used by
// nested service value types before Goa chooses final import names.
package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goagenerator "goa.design/goa/v3/codegen/generator"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// TestMCPRegistryPlansNestedTypeImports catches registry files that discover
// relocated Goa types or custom Go types only after import names are final.
func TestMCPRegistryPlansNestedTypeImports(t *testing.T) {
	const version = "1.0"

	goaAIDirectory := testModuleDirectory(t, "goa.design/goa-ai")
	goaDirectory := testModuleDirectory(t, "goa.design/goa/v3")
	t.Setenv("GOWORK", "off")
	restoreMCP := resetMCPCodegenState(t)
	defer restoreMCP()
	previousRoot := expr.Root
	defer func() {
		expr.Root = previousRoot
		eval.Reset()
	}()

	service, methods := testService("catalog", "inspect")
	relocated := &expr.UserTypeExpr{
		TypeName: "RelocatedItem",
		UID:      "mcp-registry-relocated-item",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "name", Attribute: &expr.AttributeExpr{Type: expr.String}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"name"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"catalog/items"}},
		},
	}
	methods["inspect"].Payload = &expr.AttributeExpr{Type: &expr.Array{
		ElemType: &expr.AttributeExpr{Type: relocated},
	}}
	methods["inspect"].Result = &expr.AttributeExpr{Type: &expr.Array{
		ElemType: &expr.AttributeExpr{
			Type: expr.Int64,
			Meta: expr.MetaExpr{
				"struct:field:type": {"time.Duration", "time", "time"},
			},
		},
	}}

	root := testRootExpr(
		[]*expr.ServiceExpr{service},
		[]*expr.HTTPServiceExpr{jsonrpcService(service, "/catalog")},
	)
	root.API.Name = "catalog"
	root.API.Version = version
	root.API.GRPC = &expr.GRPCExpr{}
	root.API.RandomizerFactory = expr.NewDeterministicRandomizerFactory()
	root.Types = append(root.Types, relocated)
	root.WalkSets(func(eval.ExpressionSet) {})
	for _, method := range service.Methods {
		method.Prepare()
	}
	expr.Root = root
	eval.Reset()
	require.NoError(t, eval.Register(root))
	require.NoError(t, eval.Register(mcpexpr.Root))
	mcpexpr.Root.RegisterMCP(service, &mcpexpr.MCPExpr{
		Name:    "catalog",
		Version: version,
		Tools: []*mcpexpr.ToolExpr{
			{Name: "inspect", Method: methods["inspect"]},
		},
	})
	for _, server := range mcpexpr.Root.MCPServers {
		server.Finalize()
	}

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte(fmt.Sprintf(`module generated.local

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
	_, err := goagenerator.Generate(dir, "gen", false)
	require.NoError(t, err)
	generatedRoot, err := os.OpenRoot(filepath.Join(dir, "gen"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, generatedRoot.Close())
	})
	register, err := generatedRoot.ReadFile("mcp_catalog/register.go")
	require.NoError(t, err)
	require.Contains(t, string(register), `items "generated.local/gen/catalog/items"`)
	require.Contains(t, string(register), `"time"`)
	require.Contains(t, string(register), `[]*items.RelocatedItem`)
	require.Contains(t, string(register), `[]time.Duration`)
}
