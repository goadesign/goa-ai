// Resolved service calls preserve provider input and selected registration across retries.
// Generated client/server conversion and validation check the actual wire fields.
package runtime

import (
	"context"
	"goa.design/goa-ai/internal/registrycall"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"google.golang.org/protobuf/proto"
)

func TestResolvedRegistryClientPreservesProviderArgumentsAcrossRetry(t *testing.T) {
	enriched := []byte(`{"query":"active","answer":"approved"}`)
	token := strings.Repeat("a", 64)
	var received []*genregistry.CallResolvedToolPayload
	client := registrycall.NewResolvedClient(&genregistry.Client{CallResolvedToolEndpoint: func(_ context.Context, value any) (any, error) {
		request := value.(*genregistry.CallResolvedToolPayload)
		encoded := genregistryclient.NewProtoCallResolvedToolRequest(request)
		require.NoError(t, genregistryclient.ValidateCallResolvedToolRequest(encoded))
		wire, err := proto.Marshal(encoded)
		require.NoError(t, err)
		var decoded genregistrypb.CallResolvedToolRequest
		require.NoError(t, proto.Unmarshal(wire, &decoded))
		require.NoError(t, genregistryserver.ValidateCallResolvedToolRequest(&decoded))
		restored := genregistryserver.NewCallResolvedToolPayload(&decoded)
		assert.Equal(t, enriched, restored.PayloadJSON)
		assert.Equal(t, token, restored.ExpectedRegistrationToken)
		assert.Equal(t, uint64(1), restored.Meta.ExecutionSequence)
		received = append(received, restored)
		return &genregistry.CallToolResult{RegistrationToken: token}, nil
	}}, token, "records.read")
	payload := &genregistry.CallToolPayload{Toolset: "provider.records", Tool: "records.read", PayloadJSON: enriched, Meta: &genregistry.ToolCallMeta{RunID: "root", SessionID: new("session"), ToolCallID: "call", ExecutionSequence: 1}, WireProtocolVersion: toolregistry.WireProtocolVersion}
	_, err := client.CallTool(t.Context(), payload)
	require.NoError(t, err)
	_, err = client.RetryTool(t.Context(), &genregistry.RetryToolPayload{ExpectedRegistrationToken: token, Toolset: payload.Toolset, Tool: payload.Tool, PayloadJSON: payload.PayloadJSON, Meta: payload.Meta, WireProtocolVersion: payload.WireProtocolVersion})
	require.NoError(t, err)
	require.Len(t, received, 2)
	assert.Equal(t, received[0], received[1])
}
