package types_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
	grpccodegen "goa.design/goa/v3/grpc/codegen"
	"goa.design/goa/v3/http/codegen/openapi"
	openapiv3 "goa.design/goa/v3/http/codegen/openapi/v3"

	registrytypes "goa.design/goa-ai/registry/design/types"
)

func TestTypesComposeWithoutRegisteringService(t *testing.T) {
	require.Nil(t, expr.Root.API)
	require.Empty(t, expr.Root.Services)

	domainTypes := make([]expr.UserType, 0, 9)
	for _, name := range []string{"RegisteredToolset", "InputContinuation", "TaskAnswers", "ExecutionContinuation", "HostRequest", "PendingInput", "TaskWait", "TaskInput", "PendingExecution"} {
		domainType := dsl.Type(name, func() {
			dsl.Field(1, "value", dsl.String, "Application-owned value.")
			dsl.Required("value")
		})
		require.NotNil(t, domainType, "the imported framework schema must leave %s available to the application", name)
		domainTypes = append(domainTypes, domainType)
	}
	// An application may put its catalog records in a shared package. Importing
	// registry schemas must let that record and its fields share that package.
	catalogEntry := dsl.Type("CatalogEntry", func() {
		dsl.Meta("struct:pkg:path", "types")
		dsl.Field(1, "resolved", registrytypes.ResolvedToolset)
		dsl.Required("resolved")
	})
	dsl.API("catalog", func() {})
	dsl.Service("catalog", func() {
		for _, domainType := range domainTypes {
			dsl.Method("domain_"+domainType.Name(), func() {
				dsl.Payload(domainType)
				dsl.GRPC(func() {})
				dsl.HTTP(func() {
					dsl.POST("/" + domainType.Name())
				})
			})
		}
		dsl.Method("entry", func() {
			dsl.Result(catalogEntry)
			dsl.GRPC(func() {})
		})
		dsl.Method("register", func() {
			dsl.Payload(registrytypes.AgentToolsetDeclaration)
			dsl.Result(registrytypes.ResolvedToolset)
			dsl.GRPC(func() {})
			dsl.HTTP(func() {
				dsl.POST("/register")
			})
		})
		dsl.Method("report_output", func() {
			dsl.Payload(registrytypes.PublishToolOutputDeltaPayload)
			dsl.GRPC(func() {})
		})
		dsl.Method("get_call_context", func() {
			dsl.Result(registrytypes.ToolCallMeta)
			dsl.GRPC(func() {})
		})
	})
	require.NoError(t, eval.RunDSL())
	require.Len(t, expr.Root.Services, 1)
	assert.Equal(t, "catalog", expr.Root.Services[0].Name)
	assert.Equal(t, "catalog", expr.Root.API.Name)

	generation, err := codegen.NewGeneration("catalog.example/gen", []eval.Root{expr.Root})
	require.NoError(t, err)
	servicePlan, err := service.NewPlan(expr.Root, generation, expr.NewExampleGenerator(expr.Root.API.RandomizerFactory))
	require.NoError(t, err)
	grpcPlans, err := grpccodegen.NewPlans(generation, grpccodegen.PlanInput{Root: expr.Root, Service: servicePlan})
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.NoError(t, servicePlan.Link())
	require.NoError(t, grpcPlans[0].Link())
	files, err := service.Files(servicePlan)
	require.NoError(t, err)
	files = append(files, grpcPlans[0].ProtoFiles()...)
	output := t.TempDir()
	for _, file := range files {
		_, err := file.Render(output)
		require.NoErrorf(t, err, "render %s", file.Path)
	}
	generated, err := os.ReadFile(filepath.Join(output, "gen", "catalog", "service.go"))
	require.NoError(t, err)
	assert.Contains(t, string(generated), "type RegisteredToolset struct")
	sharedFiles, err := filepath.Glob(filepath.Join(output, "gen", "types", "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, sharedFiles)
	var shared string
	for _, filename := range sharedFiles {
		content, err := os.ReadFile(filename)
		require.NoError(t, err)
		shared += string(content)
	}
	assert.Contains(t, shared, "type CatalogEntry struct")
	assert.Contains(t, shared, "type ResolvedToolset struct")
	assert.Contains(t, shared, "type Toolset struct")
	assert.Contains(t, shared, "RegistrationToken string")
	protoFiles := grpcPlans[0].ProtoFiles()
	require.Len(t, protoFiles, 1)
	protobuf, err := os.ReadFile(filepath.Join(output, protoFiles[0].Path))
	require.NoError(t, err)
	for _, field := range []string{"Toolset toolset = 1;", "optional string registration_token = 2;", "repeated ToolSchema tools = 5;", "optional string registered_at = 6;"} {
		assert.Contains(t, string(protobuf), field)
	}

	// Importing registry types must keep saved workflow values out of the HTTP
	// document while still publishing the application's actual endpoints.
	for _, version := range []openapi.Version{openapi.Version30, openapi.Version32} {
		spec := openapiv3.New(expr.Root, version)
		require.NotNil(t, spec)
		assert.NotEmpty(t, spec.Paths)
		assert.Contains(t, spec.Components.Schemas, "ResolvedToolset")
		assert.Contains(t, spec.Components.Schemas, "Toolset")
		for _, name := range []string{"ToolOperationPendingExecution", "ToolOperationPendingInput", "ToolOperationHostRequest", "ToolOperationTaskWait", "ToolOperationTaskInput"} {
			assert.NotContains(t, spec.Components.Schemas, name)
		}
	}
}
