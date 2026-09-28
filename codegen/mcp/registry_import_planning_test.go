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
	goacodegen "goa.design/goa/v3/codegen"
	goagenerator "goa.design/goa/v3/codegen/generator"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/dsl"
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

// TestMCPRegistryUsesRetainedReferenceImports checks the real method layouts
// used for codecs before registration submits imports to its own output package.
func TestMCPRegistryUsesRetainedReferenceImports(t *testing.T) {
	for _, useParent := range []bool{false, true} {
		name := "direct inherited child"
		if useParent {
			name = "named parent excludes hidden child"
		}
		t.Run(name, func(t *testing.T) {
			var child expr.UserType
			root := goacodegen.RunDSL(t, func() {
				dsl.API("imports", func() {})
				child = dsl.Type("Child", func() {
					if useParent {
						dsl.Meta("struct:pkg:path", "a/types")
					}
					dsl.Attribute("value", dsl.String)
					dsl.Required("value")
				})
				parent := dsl.Type("Parent", func() {
					dsl.Meta("struct:pkg:path", "z/types")
					dsl.Meta("type:generate:force")
					dsl.Attribute("child", child)
				})
				selected := child
				if useParent {
					selected = parent
				}
				dsl.Service("values", func() {
					dsl.Method("exchange", func() {
						dsl.Payload(selected)
						dsl.Result(selected)
					})
				})
			})
			generation, err := goacodegen.NewGeneration("generated.local/gen", []eval.Root{root})
			require.NoError(t, err)
			services, err := goaservice.NewPlan(root, generation, expr.NewExampleGenerator(root.API.RandomizerFactory))
			require.NoError(t, err)
			service := root.Service("values")
			method := service.Method("exchange")
			prepared := &preparedMCPService{
				userService: service,
				mcp: &mcpexpr.MCPExpr{
					Tools: []*mcpexpr.ToolExpr{{Name: "exchange", Method: method}},
				},
			}
			output, err := generation.ClaimPackage("generated.local/gen/mcp_values")
			require.NoError(t, err)
			data := &AdapterData{
				mcpImportPath: output.ImportPath(),
				mcpPackage:    output,
				Tools: []*ToolAdapter{{
					userMethodName: "exchange", HasPayload: true, HasResult: true,
				}},
			}
			codecs, methods, err := planMCPCodecs(generation, services, prepared, data)
			require.NoError(t, err)
			require.NotNil(t, codecs)
			values := methods["exchange"]
			require.NotNil(t, values)
			payload, result := values.payloadLayout, values.resultLayout
			require.NotNil(t, payload)
			require.NotNil(t, result)
			require.True(t, payload.MatchesOccurrence(method.Payload))
			require.True(t, result.MatchesOccurrence(method.Result))
			require.Equal(t, "generated.local/gen/z/types", payload.Owner())
			require.Equal(t, payload.Owner(), result.Owner())
			require.NoError(t, planMCPRegisterTypeImports(prepared, data, methods))
			require.Same(t, payload, values.payloadLayout)
			require.Same(t, result, values.resultLayout)
			require.Equal(t, []string{"generated.local/gen/z/types"}, data.registerImportPaths)
			require.NoError(t, generation.Freeze())
			require.Equal(t, "types", output.ImportName("generated.local/gen/z/types"))
			if useParent {
				require.PanicsWithValue(t, `import path "generated.local/gen/a/types" has no planned alias`, func() {
					output.ImportName("generated.local/gen/a/types")
				})
			} else {
				require.NotContains(t, child.Attribute().Meta, "struct:pkg:path")
			}
		})
	}
}

func TestMCPRegistryNoValueImports(t *testing.T) {
	root := goacodegen.RunDSL(t, func() {
		dsl.Service("values", func() {
			dsl.Method("ping", func() {})
		})
	})
	generation, err := goacodegen.NewGeneration("generated.local/gen", []eval.Root{root})
	require.NoError(t, err)
	prepared := &preparedMCPService{
		mcp: &mcpexpr.MCPExpr{
			Tools: []*mcpexpr.ToolExpr{{Name: "ping", Method: root.Service("values").Method("ping")}},
		},
	}
	output, err := generation.ClaimPackage("generated.local/gen/mcp_values")
	require.NoError(t, err)
	data := &AdapterData{mcpPackage: output}
	require.NoError(t, planMCPRegisterTypeImports(prepared, data, nil))
	require.Empty(t, data.registerImportPaths)
}
