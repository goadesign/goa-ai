// These tests exercise the registry's generated request and response converters.
// Nullable entries keep their positions in memory; required-entry validation
// and protobuf's separate wire representation keep their existing rules.
package registry

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestRegistryNullableCollectionsKeepPositions(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		change func(*genregistry.Toolset)
	}{
		{"tools", func(v *genregistry.Toolset) { v.Tools = append(v.Tools, nil, v.Tools[0]) }},
		{"fields", func(v *genregistry.Toolset) {
			p := v.Tools[0].ConsumerContract.Payload
			p.Fields = append(p.Fields, nil, p.Fields[0])
		}},
		{"path", func(v *genregistry.Toolset) {
			f := v.Tools[0].ConsumerContract.Payload.Fields[0]
			f.Path = append(f.Path, nil, f.Path[0])
		}},
		{"branches", func(v *genregistry.Toolset) {
			v.Tools[0].ConsumerContract.Payload.Fields[0].Branches = []*genregistry.ToolUnionBranch{
				{Selection: genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{
					Value: "first",
					Discriminator: []*genregistry.ToolFieldPathSegment{
						{Segment: genregistry.NewToolFieldSegmentField("kind")}, nil,
					},
				})}, nil,
				{Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object"})},
			}
		}},
		{"server data", func(v *genregistry.Toolset) {
			v.Tools[0].ConsumerContract.ServerData = []*genregistry.ToolServerData{
				{Kind: "first", Audience: "user", Schema: []byte(`{}`)}, nil,
				{Kind: "last", Audience: "user", Schema: []byte(`{}`)},
			}
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			toolset := testDefinitionToolset()
			toolset.RegisteredAt = "2026-01-01T00:00:00Z"
			fixture.change(toolset)
			request := &genregistry.AgentToolsetDeclaration{Name: toolset.Name, Tools: toolset.Tools}
			encoded := genregistryclient.NewProtoRegisterAgentToolsetRequest(request)
			assert.Equal(t, request, genregistryserver.NewRegisterAgentToolsetPayload(encoded))
			response := &genregistry.ResolvedToolset{Toolset: toolset, RegistrationToken: strings.Repeat("1", 64)}
			wireResponse := genregistryserver.NewProtoRegisterAgentToolsetResponse(response)
			assert.Equal(t, response, genregistryclient.NewRegisterAgentToolsetResult(wireResponse))
		})
	}
}

func TestRegistryRequiredEntriesKeepValidation(t *testing.T) {
	declaration := testAgentToolset("v1")
	payload := &genregistry.AgentExportsDeclaration{
		Scope: "owner", ResourceName: "native", Name: declaration.Name, Exports: declaration.Tools,
	}
	request := genregistryclient.NewProtoRegisterAgentExportsRequest(payload)
	require.NoError(t, genregistryserver.ValidateRegisterAgentExportsRequest(request))
	assert.Equal(t, payload, genregistryserver.NewRegisterAgentExportsPayload(request))
	request.Exports = append(request.Exports, nil)
	assert.Error(t, genregistryserver.ValidateRegisterAgentExportsRequest(request))
}

func TestRegistryNullableEntriesKeepWireRules(t *testing.T) {
	request := genregistryclient.NewProtoRegisterAgentToolsetRequest(&genregistry.AgentToolsetDeclaration{
		Name: "tools", Tools: []*genregistry.ToolSchema{nil},
	})
	require.Len(t, request.Tools, 1)
	assert.Nil(t, request.Tools[0])
	data, err := proto.Marshal(request)
	require.NoError(t, err)
	decoded := new(genregistrypb.RegisterAgentToolsetRequest)
	require.NoError(t, proto.Unmarshal(data, decoded))
	require.Len(t, decoded.Tools, 1)
	assert.NotNil(t, decoded.Tools[0], "protobuf encodes a nil message entry as a present empty message")
	assert.Error(t, protojson.Unmarshal([]byte(`{"name":"tools","tools":[null]}`), decoded))
}
