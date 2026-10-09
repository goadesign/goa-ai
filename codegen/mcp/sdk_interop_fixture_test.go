// Package codegen supplies a synthetic service and independent SDK peers. Both
// directions use real HTTP handlers and schema codecs; the assertions observe
// caller-visible values rather than inspect generated source text.
package codegen

const sdkInteropDesign = `package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("sdk-peer", func() { Title("Independent MCP interoperability") })
var _ = Service("records", func() {
	Description("Reads synthetic records for independent protocol checks.")
	JSONRPC(func() { POST("/mcp") })
	MCP("records", "1")
	Method("read", func() {
		Description("Returns a record identifier supplied by the caller.")
		Payload(func() { Field(1, "name", String, "Record identifier", func() { MinLength(1) }); Required("name") })
		Result(String)
		Tool("read", "Read a synthetic record", func() { ReadOnlyHint(true) })
	})
	Method("document", func() {
		Description("Reads the synthetic reference document.")
		Result(String)
		Resource("document", "record://reference", "text/plain")
	})
	StaticPrompt("welcome", "Synthetic welcome", "user", "Read the reference.")
})
`

const sdkInteropRuntime = `package sdkpeer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
	genserver "sdk-peer.local/gen/jsonrpc/mcp_records/server"
	genmcp "sdk-peer.local/gen/mcp_records"
	genrecords "sdk-peer.local/gen/records"
)

type (
	recordService struct {
		calls atomic.Int64
	}
)

func (s *recordService) Read(_ context.Context, p *genrecords.ReadPayload) (string, error) {
	s.calls.Add(1)
	if p.Name == "denied" {
		return "", goa.PermanentError("record_denied", "record denied")
	}
	return p.Name, nil
}
func (s *recordService) Document(context.Context) (string, error) { return "reference", nil }

func TestSDKClientReadsGeneratedServer(t *testing.T) {
	service := &recordService{}
	endpoints := genrecords.NewEndpoints(service)
	var middleware atomic.Int64
	endpoints.Use(func(next goa.Endpoint) goa.Endpoint {
		return func(ctx context.Context, input any) (any, error) { middleware.Add(1); return next(ctx, input) }
	})
	adapter := genmcp.NewMCPAdapter(endpoints, nil)
	mux := goahttp.NewMuxer()
	server := genserver.New(genmcp.NewEndpoints(adapter), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil)
	genserver.Mount(mux, server)
	peer := httptest.NewServer(mux)
	defer peer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "independent-peer", Version: "1"}, &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
	})
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: peer.URL + "/mcp", HTTPClient: peer.Client(), MaxRetries: -1}, &sdk.ClientSessionOptions{ProtocolVersion: mcpruntime.ProtocolVersion})
	require.NoError(t, err)
	defer func() { assert.NoError(t, session.Close()) }()
	assert.Equal(t, mcpruntime.ProtocolVersion, session.InitializeResult().ProtocolVersion)
	catalog, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, catalog.Tools, 1)
	assert.Equal(t, "read", catalog.Tools[0].Name)
	assert.Equal(t, "private", catalog.CacheScope)
	result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"record-one\"}")})
	require.NoError(t, err)
	assert.Equal(t, "record-one", result.StructuredContent)
	assert.False(t, result.IsError)
	assert.EqualValues(t, 1, service.calls.Load())
	assert.EqualValues(t, 1, middleware.Load())
	invalid, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"\"}")})
	require.NoError(t, err)
	assert.True(t, invalid.IsError)
	assert.EqualValues(t, 1, service.calls.Load())
	denied, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"denied\"}")})
	require.NoError(t, err)
	assert.True(t, denied.IsError)
	_, err = session.CallTool(t.Context(), &sdk.CallToolParams{Name: "absent", Arguments: json.RawMessage("{}")})
	assert.Error(t, err)
	document, err := session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "record://reference"})
	require.NoError(t, err)
	require.Len(t, document.Contents, 1)
	assert.Equal(t, "reference", document.Contents[0].Text)
	prompts, err := session.ListPrompts(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, prompts.Prompts, 1)
	prompt, err := session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "welcome"})
	require.NoError(t, err)
	require.Len(t, prompt.Messages, 1)
	text, ok := prompt.Messages[0].Content.(*sdk.TextContent)
	require.True(t, ok)
	assert.Equal(t, "Read the reference.", text.Text)
}

func TestFrameworkCallerCompletesSDKInputRound(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "independent-server", Version: "1"}, &sdk.ServerOptions{SupportedProtocolVersions: []string{mcpruntime.ProtocolVersion}})
	var rounds atomic.Int64
	server.AddTool(&sdk.Tool{Name: "select", InputSchema: json.RawMessage("{\"type\":\"object\",\"properties\":{},\"additionalProperties\":false}"), OutputSchema: json.RawMessage("{\"type\":\"string\"}")}, func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		rounds.Add(1)
		// The first round asks for a label; the next round must carry the same
		// state and the typed host answer before returning a completed result.
		if request.Params.RequestState == "" {
			return &sdk.CallToolResult{RequestState: "opaque-for-this-invocation", InputRequests: sdk.InputRequestMap{
				"question": &sdk.ElicitParams{Mode: "form", Message: "Choose a label", RequestedSchema: json.RawMessage("{\"type\":\"object\",\"properties\":{\"label\":{\"type\":\"string\"}},\"required\":[\"label\"]}")},
			}}, nil
		}
		if request.Params.RequestState != "opaque-for-this-invocation" {
			return nil, errors.New("state changed")
		}
		response, ok := request.Params.InputResponses["question"].(*sdk.ElicitResult)
		if !ok || response.Action != "accept" || response.Content["label"] != "accepted" {
			return nil, errors.New("answer changed")
		}
		return &sdk.CallToolResult{Content: []sdk.Content{}, StructuredContent: "accepted"}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true})
	peer := httptest.NewServer(handler)
	defer peer.Close()
	caller, err := mcpruntime.NewHTTPCaller(mcpruntime.HTTPOptions{Endpoint: peer.URL, Client: peer.Client(), ClientInfo: mcpruntime.ClientInfo{Name: "framework-peer", Version: "1"}, InputSupport: mcpruntime.InputSupport{Form: true}})
	require.NoError(t, err)
	initial, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{Tool: "select", Payload: json.RawMessage("{}")})
	require.NoError(t, err)
	require.NotNil(t, initial.InputRequired)
	assert.Empty(t, initial.StructuredContent)
	assert.EqualValues(t, 1, rounds.Load())
	answers := map[string]json.RawMessage{"question": json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"accepted\"}}")}
	require.NoError(t, initial.InputRequired.ValidateResponses(answers))
	complete, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{Tool: "select", Payload: json.RawMessage("{}"), Continuation: &mcpruntime.CallContinuation{RequestState: initial.InputRequired.RequestState, InputResponses: answers}})
	require.NoError(t, err)
	assert.Nil(t, complete.InputRequired)
	assert.JSONEq(t, "\"accepted\"", string(complete.StructuredContent))
	assert.EqualValues(t, 2, rounds.Load())
}
`
