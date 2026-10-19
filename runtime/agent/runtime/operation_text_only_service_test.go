// Service-operation tests exercise the real Registry transport and the workflow
// checks that reject host interaction without repeating a completed provider call.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/internal/tooloperation"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestExactServiceTextOnlyDispatchAndResult(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		for _, ui := range []bool{false, true} {
			t.Run(fmt.Sprintf("text-only=%t/ui=%t", restricted, ui), func(t *testing.T) {
				resolved, invocation := textOnlyOperationFixture(false)
				store := newTestStore()
				rt := New(store, WithEngine(engineinmem.New()), WithToolWorkflowQueue("worker"))
				pulse := mockpulse.NewClient(t)
				var resultBytes []byte
				calls := 0
				client := &genregistry.Client{CallResolvedToolEndpoint: func(_ context.Context, value any) (any, error) {
					calls++
					request := value.(*genregistry.CallResolvedToolPayload)
					assert.Equal(t, restricted, request.Meta.TextOnly)
					assert.Equal(t, invocation.Arguments, request.PayloadJSON)
					message := toolregistry.NewToolResultMessage(resolved.RegistrationToken, "result-use", json.RawMessage(`{"count":2}`))
					if ui {
						message.ServerData = []*toolregistry.ServerDataItem{{Kind: "record.card", Audience: "timeline", Data: json.RawMessage(`{"count":2}`)}}
					}
					var err error
					resultBytes, err = json.Marshal(message)
					require.NoError(t, err)
					now := time.Now().Truncate(time.Millisecond)
					return &genregistry.CallToolResult{ToolUseID: "result-use", RegistrationToken: resolved.RegistrationToken, ExecutionDeadline: now.Add(toolregistry.MaxToolCallWait).Format(time.RFC3339Nano), ResultStreamExpiresAt: now.Add(toolregistry.DefaultResultStreamTTL).Format(time.RFC3339Nano)}, nil
				}}
				pulse.SetStream(func(name string, _ ...streamopts.Stream) (pulsec.Stream, error) {
					assert.Equal(t, toolregistry.ResultStreamID("result-use"), name)
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
				require.NoError(t, rt.RegisterRegistry("registry", client, pulse))
				require.NoError(t, rt.RegisterToolWorkflow(t.Context()))
				request, err := rt.prepareRegisteredRootRequest("", "registry", resolved, invocation, nil, nil, WithTextOnly(restricted))
				require.NoError(t, err)
				_, state, err := restoreRegisteredRoot(request.Input)
				require.NoError(t, err)
				handle, err := rt.startWorkflow(t.Context(), request)
				require.NoError(t, err)
				result, err := handle.Wait(t.Context())
				if restricted && ui {
					require.Error(t, err)
					records, readErr := store.ListRunRecords(t.Context(), "service-root", "", 100)
					require.NoError(t, readErr)
					for _, record := range records.Events {
						assert.NotEqual(t, string(hooks.ToolResultReceived), string(record.Type))
					}
				} else {
					require.NoError(t, err)
					require.NotNil(t, result.FinalToolResult)
					assert.JSONEq(t, `{"count":2}`, string(result.FinalToolResult.Result))
				}
				assert.Equal(t, 1, calls)
				state.call.TextOnly = !restricted
				_, err = rt.executeRegisteredRootActivity(t.Context(), &state.call)
				require.ErrorContains(t, err, "changed original execution identity or input")
				assert.True(t, engine.IsActivityErrorNonRetryable(err))
				assert.Equal(t, 1, calls)
			})
		}
	}
}

func TestExactServiceTextOnlyWorkflowRejectsHostInputButAllowsTasks(t *testing.T) {
	for _, hostInput := range []bool{false, true} {
		t.Run(fmt.Sprintf("host-input=%t", hostInput), func(t *testing.T) {
			resolved, invocation := textOnlyOperationFixture(false)
			calls := 0
			backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(_ context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
				assert.True(t, input.TextOnly)
				calls++
				if calls > 1 {
					return &api.ToolOutput{Payload: []byte(`{"count":2}`)}, nil
				}
				var pending *api.PendingExecution
				var err error
				if hostInput {
					pending, err = tooloperation.NewPendingInput(&mcp.InputRequired{RequestState: new("state")})
				} else {
					pending, err = api.NewPendingTaskWait("provider-task", new(int64(0)))
				}
				return &api.ToolOutput{PendingExecution: pending}, err
			}}
			rt := New(newTestStore(), WithEngine(backend), WithToolWorkflowQueue("worker"))
			require.NoError(t, rt.RegisterToolWorkflow(t.Context()))
			handle, err := rt.StartOneShotToolOperation(t.Context(), "registry", resolved, invocation, nil, nil, WithTextOnly(true))
			require.NoError(t, err)
			output, err := handle.Wait(t.Context())
			if hostInput {
				require.ErrorContains(t, err, "cannot request host input")
				assert.Equal(t, 1, calls)
			} else {
				require.NoError(t, err)
				require.NotNil(t, output.FinalToolResult)
				assert.Equal(t, 2, calls)
			}
		})
	}
}

func TestExactServiceTextOnlyRejectsRuntimeConfirmation(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(fmt.Sprintf("text-only=%t", restricted), func(t *testing.T) {
			resolved, invocation := textOnlyOperationFixture(false)
			calls, rendered := 0, 0
			backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(context.Context, *api.ToolInput) (*api.ToolOutput, error) {
				calls++
				return &api.ToolOutput{Payload: []byte(`{"count":2}`)}, nil
			}}
			store := newTestStore()
			rt := New(store, WithEngine(backend), WithToolWorkflowQueue("worker"), WithToolConfirmation(&ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{
				genrecords.Read: {
					Prompt:       func(context.Context, *ToolCall) (string, error) { rendered++; return "Read records?", nil },
					DeniedResult: func(context.Context, *ToolCall) (any, error) { return map[string]int{"count": 0}, nil },
				},
			}}))
			require.NoError(t, rt.RegisterToolWorkflow(t.Context()))
			handle, err := rt.StartOneShotToolOperation(t.Context(), "registry", resolved, invocation, nil, nil, WithTextOnly(restricted))
			require.NoError(t, err)
			output, err := handle.Wait(t.Context())
			if restricted {
				require.ErrorContains(t, err, "text-only tools cannot require confirmation")
				assert.Nil(t, output)
				assert.Zero(t, rendered)
			} else {
				require.NoError(t, err)
				require.NotNil(t, output.Suspension)
				assert.Equal(t, 1, rendered)
			}
			assert.Zero(t, calls)
		})
	}
}
