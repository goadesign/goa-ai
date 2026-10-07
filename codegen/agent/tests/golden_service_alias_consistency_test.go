package tests

import (
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TestGolden_ServiceAlias_Consistency checks that generated tool code imports
// an underscored service with the same package name used in Go references.
func TestGolden_ServiceAlias_Consistency(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, func() {
		// Service name contains underscore to exercise alias vs path base.
		API("catalog_agent", func() {})

		// Define a user type at API scope, referenced directly by tool payload/result.
		var Doc = Type("Doc", func() {
			Attribute("id", String, "ID")
			Required("id")
		})

		Service("catalog_agent", func() {
			Method("read", func() {
				Payload(Doc)
				Result(Doc)
			})
			Agent("reader", "", func() {
				Use("docs", func() {
					Tool("read", "Read", func() {
						Args(Doc)
						Return(Doc)
						BindTo("read")
					})
				})
			})
		})
	})

	provider := renderedFileContent(t, files, "gen/catalog_agent/toolsets/docs/provider.go")
	transforms := renderedFileContent(t, files, "gen/catalog_agent/toolsets/docs/transforms.go")
	codecs := fileContent(t, files, "gen/catalog_agent/toolsets/docs/codecs.go")
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "gen/catalog_agent/agents/reader/docs/named_binding_test.go", namedBindingRuntimeTest)
	runGeneratedPackageTest(t, root, "./gen/catalog_agent/...")
	assertGoldenGo(t, "service_alias_consistency", "provider.go.golden", provider)
	assertGoldenGo(t, "service_alias_consistency", "transforms.go.golden", transforms)
	assertGoldenGo(t, "service_alias_consistency", "codecs.go.golden", codecs)
}

// namedBindingRuntimeTest checks actual Go values, rather than matching DSL names.
const namedBindingRuntimeTest = `package docs_test

import (
 "context"
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 gencatalog "generated.local/gen/catalog_agent"
 gendocs "generated.local/gen/catalog_agent/toolsets/docs"
 genexecutor "generated.local/gen/catalog_agent/agents/reader/docs"
 "goa.design/goa-ai/runtime/agent/runtime"
)

type documentService struct{}

func (*documentService)Read(_ context.Context,p *gencatalog.Doc)(*gencatalog.Doc,error) {
 return &gencatalog.Doc{ID:p.ID},nil
}

func TestNamedServiceBinding(t *testing.T) {
 service:=&documentService{}
 executor:=genexecutor.NewReaderDocsExec(genexecutor.WithClient(gencatalog.NewClient(gencatalog.NewEndpoints(service).Read)))
 outcome,err:=executor.Execute(t.Context(),&runtime.ToolCallMeta{},&runtime.ToolCall{Name:gendocs.Read,Payload:[]byte("{\"id\":\"kept\"}")})
 require.NoError(t,err)
 require.NotNil(t,outcome.ToolResult)
 require.Nil(t,outcome.ToolResult.Failure)
 value,ok:=outcome.ToolResult.Result.(*gendocs.ReadResult)
 require.True(t,ok)
 assert.Equal(t,"kept",value.ID)
 _,err=gendocs.ReadResultCodec().ToJSON(value)
 assert.NoError(t,err)
}
`
