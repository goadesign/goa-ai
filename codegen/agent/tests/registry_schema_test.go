// These tests compile the emitted registration factories and compare their
// declarations with the generated local contracts through the actual wire types.
package tests

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestGeneratedRegistrySchemaFactory(t *testing.T) {
	cases := []struct {
		name   string
		design func() func()
	}{
		{"bounds", testscenarios.ServiceToolsetBindSelfBoundedResult},
		{"server-data", testscenarios.ServiceToolsetBindSelfServerData},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			files := buildCompleteGeneratedFiles(t, test.design())
			source := generatedContentBySuffix(t, files, "toolsets/lookup/registry_schemas.go")
			syntax, err := parser.ParseFile(token.NewFileSet(), "registry_schemas.go", source, 0)
			require.NoError(t, err)
			ast.Inspect(syntax, func(node ast.Node) bool {
				switch node.(type) {
				case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt:
					t.Fatal("static registry declarations must not contain runtime branches or loops")
				}
				return true
			})
			// Keep each tool, type, and field in its own constructor. Combining
			// their literals into one function makes large catalogs slow to compile.
			for _, declaration := range syntax.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok {
					continue
				}
				records := 0
				ast.Inspect(function.Body, func(node ast.Node) bool {
					literal, ok := node.(*ast.CompositeLit)
					if !ok {
						return true
					}
					selector, ok := literal.Type.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch selector.Sel.Name {
					case "ToolSchema", "ToolTypeMetadata", "ToolFieldMetadata":
						records++
					}
					return true
				})
				assert.LessOrEqual(t, records, 1, "constructor %s combines generated records", function.Name.Name)
			}
			require.NotContains(t, source, "json.Unmarshal")
			require.NotContains(t, source, "Specs()")
			root := writeCompleteGeneratedModule(t, files)
			writeGeneratedPackageTest(t, root, "registrycontract/contract_test.go", generatedRegistrySchemaTest)
			runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./registrycontract"))
			// #nosec G304 -- root is the test-owned generated module directory.
			encoded, err := os.ReadFile(filepath.Join(root, "registrycontract", "contracts.json"))
			require.NoError(t, err)
			var generated struct {
				Declarations []*genregistry.ToolSchema
				Fingerprint  string
				Toolset      string
			}
			require.NoError(t, json.Unmarshal(encoded, &generated))
			admission := internaladmission.Schema{Name: generated.Toolset}
			for _, declaration := range generated.Declarations {
				contract, err := json.Marshal(declaration.ConsumerContract)
				require.NoError(t, err)
				admission.Tools = append(admission.Tools, internaladmission.ToolSchema{
					Name: declaration.Name, Description: declaration.Description, Tags: declaration.Tags,
					PayloadSchema: declaration.PayloadSchema, ExecutionPayloadSchema: declaration.ExecutionPayloadSchema,
					ResultSchema: declaration.ResultSchema, ConsumerContract: contract,
				})
			}
			require.Equal(t, generated.Fingerprint, internaladmission.SchemaFingerprint(admission),
				"emitted declarations must produce the precomputed registration fingerprint")
		})
	}
}

const generatedRegistrySchemaTest = `package registrycontract

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genlookup "generated.local/gen/alpha/toolsets/lookup"
	genscribe "generated.local/gen/alpha/agents/scribe"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
)

func TestRegistryDeclarations(t *testing.T) {
	declarations := genlookup.ToolSchemas()
	specs := genlookup.Specs()
	require.Len(t, declarations, len(specs))
	for i, declaration := range declarations {
		spec := specs[i]
		assert.Equal(t, spec.Name.String(), declaration.Name)
		assert.Equal(t, []byte(spec.Payload.Schema), declaration.PayloadSchema)
		assert.Equal(t, []byte(spec.ExecutionPayloadSchema), declaration.ExecutionPayloadSchema)
		assert.Equal(t, []byte(spec.Result.Schema), declaration.ResultSchema)
		contract := declaration.ConsumerContract
		require.NotNil(t, contract)
		assert.Equal(t, "service", contract.Kind)
		meta, ok := genlookup.MetadataByName(spec.Name)
		require.True(t, ok)
		assert.Equal(t, meta.Title, contract.Title)
		assert.Equal(t, spec.Search.Terms, contract.Search.Terms)
		assert.Equal(t, spec.Search.Length, contract.Search.Length)
		assert.Len(t, contract.Payload.Fields, len(spec.Payload.Fields))
		assert.Len(t, contract.ServerData, len(spec.ServerData))
		if spec.Bounds == nil {
			assert.Nil(t, contract.Bounds)
		} else {
			require.NotNil(t, contract.Bounds)
			require.NotNil(t, contract.Bounds.Paging)
			assert.Equal(t, spec.Bounds.Paging.CursorField, contract.Bounds.Paging.CursorField)
		}
	}
	resolution := &genregistry.ResolvedToolset{
		Toolset: &genregistry.Toolset{
			Name: genscribe.LookupToolsetName, RegisteredAt: "2026-09-17T00:00:00Z", Tools: declarations,
		},
		RegistrationToken: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	require.NoError(t, genregistryclient.ValidateResolveToolsetResponse(
		genregistryserver.NewProtoResolveToolsetResponse(resolution),
	))
	encoded, err := json.Marshal(resolution)
	require.NoError(t, err)
	var restored genregistry.ResolvedToolset
	require.NoError(t, json.Unmarshal(encoded, &restored))
	assert.Equal(t, resolution, &restored)
	fingerprint, err := genlookup.SchemaFingerprint(genscribe.LookupToolsetName)
	require.NoError(t, err)
	fixture, err := json.Marshal(struct {
		Declarations []*genregistry.ToolSchema
		Fingerprint string
		Toolset string
	}{declarations, fingerprint, genscribe.LookupToolsetName})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("contracts.json", fixture, 0600))
	declarations[0].ConsumerContract.Title = "changed"
	declarations[0].ConsumerContract.Search.Terms["changed"] = 100
	assert.NotEqual(t, "changed", genlookup.ToolSchemas()[0].ConsumerContract.Title)
	assert.NotContains(t, genlookup.ToolSchemas()[0].ConsumerContract.Search.Terms, "changed")
}
`
