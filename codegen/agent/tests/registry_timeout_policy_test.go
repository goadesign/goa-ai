// These tests carry authored Agent timeout policy through generated declarations,
// registry transport, and the specification used by a registry consumer.
package tests

import (
	"os/exec"
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedRegistryPreservesAgentTimeoutPolicy(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, registryTimeoutPolicyDesign())
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "timeoutpolicy/policy_test.go", generatedRegistryTimeoutPolicyTest)
	runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./timeoutpolicy"))
}

// registryTimeoutPolicyDesign exports two read operations so the timeout promise
// on one operation cannot silently become policy for the entire toolset.
func registryTimeoutPolicyDesign() func() {
	return func() {
		API("timeout-policy", func() {})
		search := Toolset("search", func() {
			Tool("find", "Find matching records.", func() {
				Args(func() {
					Attribute("query", String)
					Required("query")
				})
				Return(String)
				ReplanOnTimeout()
			})
			Tool("read", "Read a selected record.", func() {
				Args(func() {
					Attribute("query", String)
					Required("query")
				})
				Return(String)
			})
		})
		Service("reader", func() {
			Agent("reader", "Read records.", func() {
				Export(search)
			})
		})
	}
}

const generatedRegistryTimeoutPolicyTest = `package timeoutpolicy

import (
    "encoding/json"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    gensearch "generated.local/gen/reader/agents/reader/exports/search"
    genregistry "goa.design/goa-ai/registry/gen/registry"
    genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
    genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
    genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
    toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
    "google.golang.org/protobuf/proto"
)

func TestTimeoutPolicyRoundTrip(t *testing.T) {
    declarations := gensearch.ToolSchemas()
    specs := gensearch.Specs()
    require.Len(t, declarations, 2)
    require.Len(t, specs, 2)
    for i, declaration := range declarations {
        expected := declaration.Name == "search.find"
        assert.Equal(t, expected, specs[i].ReplanOnTimeout)
        assert.Equal(t, expected, declaration.ConsumerContract.ReplanOnTimeout)
        declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{
            Executor: "reader.reader", Workflow: "reader.workflow", Queue: "reader.queue", Configuration: "revision/1",
        }
    }
    request := genregistryclient.NewProtoRegisterAgentToolsetRequest(&genregistry.AgentToolsetDeclaration{
        Name: "search", Tools: declarations,
    })
    require.NoError(t, genregistryserver.ValidateRegisterAgentToolsetRequest(request))
    // An older provider omits the field; the unmarked operation must stay false.
    for _, declaration := range request.Tools {
        if *declaration.Name != "search.find" {
            declaration.ConsumerContract.ReplanOnTimeout = nil
        }
    }
    encoded, err := proto.Marshal(request)
    require.NoError(t, err)
    restored := &genregistrypb.RegisterAgentToolsetRequest{}
    require.NoError(t, proto.Unmarshal(encoded, restored))
    transported := genregistryserver.NewRegisterAgentToolsetPayload(restored)
    saved, err := json.Marshal(transported)
    require.NoError(t, err)
    var loaded genregistry.AgentToolsetDeclaration
    require.NoError(t, json.Unmarshal(saved, &loaded))
    for i, declaration := range loaded.Tools {
        spec, err := toolcontract.Compile(declaration)
        require.NoError(t, err)
        assert.Equal(t, specs[i].ReplanOnTimeout, spec.ReplanOnTimeout)
        assert.True(t, spec.IsAgentTool)
    }
}
`
