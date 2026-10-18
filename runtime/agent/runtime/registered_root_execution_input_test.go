// Registered service roots accept complete provider arguments, including paging
// inputs hidden from the model. These tests run the real workflow and registry
// executor, so validation must agree at submission, restoration, and dispatch.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestRegisteredRootUsesServiceExecutionInput(t *testing.T) {
	resolved, invocation := registeredPagingFixture()
	compiled, err := registrycontract.Resolve(resolved)
	require.NoError(t, err)
	continuation := compiled.Specs["records.continue"]
	_, err = continuation.Payload.Codec.FromJSON([]byte(`{}`))
	require.NoError(t, err)
	_, err = continuation.Payload.Codec.FromJSON([]byte(`{"query":"active","cursor":"page-2"}`))
	require.Error(t, err)

	store := newTestStore()
	runtime := New(store, WithEngine(engineinmem.New()), WithToolWorkflowQueue("service-worker"))
	pulse := mockpulse.NewClient(t)
	var expectedArguments []byte
	var expectedTool string
	var resultJSON string
	var resultBounds *agent.Bounds
	var streamID string
	var resultBytes []byte
	calls := 0
	client := &genregistry.Client{CallResolvedToolEndpoint: func(_ context.Context, value any) (any, error) {
		calls++
		request := value.(*genregistry.CallResolvedToolPayload)
		assert.Equal(t, expectedTool, request.Tool)
		assert.Equal(t, expectedArguments, request.PayloadJSON)
		assert.Equal(t, resolved.RegistrationToken, request.ExpectedRegistrationToken)
		assert.Equal(t, resolved.Toolset.Name, request.Toolset)
		assert.Nil(t, request.Meta.SessionID)
		assert.Zero(t, request.Meta.ExecutionSequence)
		assert.Nil(t, request.Meta.ExecutionContinuation)
		meta, err := store.LoadRun(t.Context(), request.Meta.RunID)
		require.NoError(t, err)
		assert.Equal(t, session.RunStatusRunning, meta.Status)
		useID := fmt.Sprintf("page-use-%d", calls)
		streamID = toolregistry.ResultStreamID(useID)
		message := toolregistry.NewToolResultMessage(resolved.RegistrationToken, useID, json.RawMessage(resultJSON))
		message.Bounds = resultBounds
		resultBytes, err = json.Marshal(message)
		require.NoError(t, err)
		now := time.Now().Truncate(time.Millisecond)
		return &genregistry.CallToolResult{
			ToolUseID: useID, RegistrationToken: resolved.RegistrationToken,
			ExecutionDeadline:     now.Add(toolregistry.MaxToolCallWait).Format(time.RFC3339Nano),
			ResultStreamExpiresAt: now.Add(toolregistry.DefaultResultStreamTTL).Format(time.RFC3339Nano),
		}, nil
	}}
	pulse.SetStream(func(name string, _ ...streamopts.Stream) (pulsec.Stream, error) {
		assert.Equal(t, streamID, name)
		stream := mockpulse.NewStream(t)
		stream.SetNewReader(func(context.Context, ...streamopts.Reader) (pulsec.Reader, error) {
			reader := mockpulse.NewReader(t)
			events := make(chan *streaming.Event, 1)
			events <- &streaming.Event{ID: "1-0", EventName: toolregistry.ResultEventKey, Payload: resultBytes}
			reader.SetSubscribe(func() <-chan *streaming.Event { return events })
			reader.SetClose(func() {})
			return reader, nil
		})
		return stream, nil
	})
	require.NoError(t, runtime.RegisterRegistry("registry", client, pulse))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))

	cursor := ""
	for index, page := range []struct {
		tool, result, next, want string
	}{
		{"records.find", `{"items":["first"]}`, "page-2", `{"items":["first"],"returned":1,"truncated":true}`},
		{"records.continue", `{"items":["second"]}`, "page-3", `{"items":["second"],"returned":1,"truncated":true}`},
	} {
		expectedTool, resultJSON = page.tool, page.result
		expectedArguments = []byte(`{"query":"active"}`)
		if index > 0 {
			expectedArguments = fmt.Appendf(nil, `{"query":"active","cursor":%q}`, cursor)
		}
		resultBounds = &agent.Bounds{Returned: 1, Truncated: true, NextCursor: &page.next}
		invocation.Arguments = expectedArguments
		root, _ := invocation.Invocation.Invocation.AsRoot()
		root.RunID = fmt.Sprintf("page-run-%d", index)
		target, _ := invocation.Target.Target.AsService()
		target.Tool = page.tool
		handle, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolved, invocation, nil, nil)
		require.NoError(t, err)
		output, err := handle.Wait(t.Context())
		require.NoError(t, err)
		require.NotNil(t, output.FinalToolResult)
		assert.Nil(t, output.FinalToolResult.Failure)
		assert.JSONEq(t, page.want, string(output.FinalToolResult.Result))
		assert.Equal(t, resultBounds, output.FinalToolResult.Bounds)
		assert.Equal(t, 1, output.ToolCount)
		assert.Nil(t, output.Final)
		cursor = *output.FinalToolResult.Bounds.NextCursor
	}
	assert.Equal(t, 2, calls)
	assert.Empty(t, runtime.agents)
}

func TestRegisteredRootRejectsIncompleteExecutionInput(t *testing.T) {
	for _, test := range []struct {
		name, arguments string
	}{
		{"missing query", `{"cursor":"page-2"}`},
		{"missing cursor", `{"query":"active"}`},
		{"model input only", `{}`},
		{"provider injection", `{"query":"active","cursor":"page-2","session_id":"caller-value"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, invocation := registeredPagingFixture()
			target, _ := invocation.Target.Target.AsService()
			target.Tool = "records.continue"
			invocation.Arguments = []byte(test.arguments)
			runtime := New(newTestStore(), WithToolWorkflowQueue("service-worker"))
			_, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolved, invocation, nil, nil)
			require.ErrorContains(t, err, "registered arguments")
		})
	}
}

// registeredPagingFixture declares a query and its dedicated continuation. The
// continuation's model input is empty, but its provider input requires the
// original query and cursor; provider-injected session_id is absent from both.
func registeredPagingFixture() (*genregistry.ResolvedToolset, *genregistry.RegisteredInvocation) {
	resolved, invocation := registeredRootFixture()
	querySchema := []byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)
	resultSchema := []byte(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"string"}},"returned":{"type":"integer"},"truncated":{"type":"boolean"}},"required":["items","returned","truncated"],"additionalProperties":false}`)
	query := resolved.Toolset.Tools[0]
	query.PayloadSchema, query.ExecutionPayloadSchema, query.ResultSchema = querySchema, querySchema, resultSchema
	query.ConsumerContract.Payload.SchemaWithoutRootExample = querySchema
	query.ConsumerContract.Result.SchemaWithoutRootExample = resultSchema
	query.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
		ContinueTool: new("records.continue"), RetainedFields: []string{"query"}, CursorField: "cursor", NextCursorField: "next_cursor",
	}}
	continuation := testRuntimeRegistryResolution("records.continue", strings.Repeat("a", 64)).Toolset.Tools[0]
	continuation.PayloadSchema = []byte(`{"type":"object","additionalProperties":false}`)
	continuation.ExecutionPayloadSchema = []byte(`{"type":"object","properties":{"query":{"type":"string"},"cursor":{"type":"string"}},"required":["query","cursor"],"additionalProperties":false}`)
	continuation.ResultSchema = resultSchema
	continuation.ConsumerContract.Payload.SchemaWithoutRootExample = continuation.PayloadSchema
	continuation.ConsumerContract.Result.SchemaWithoutRootExample = resultSchema
	continuation.ConsumerContract.Confirmation = nil
	continuation.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
		ContinueTool: new("records.continue"), SourceTool: new("records.find"), RetainedFields: []string{"query"}, CursorField: "cursor", NextCursorField: "next_cursor",
	}}
	resolved.Toolset.Tools = append(resolved.Toolset.Tools, continuation)
	return resolved, invocation
}
