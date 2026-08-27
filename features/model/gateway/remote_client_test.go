package gateway

// This file verifies that the remote gateway validates one immutable request
// contract before invoking transport callbacks. Callback mutations must not
// redefine which provider output the caller agreed to accept.

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// mustGatewayToolInput compiles a static test schema.
func mustGatewayToolInput(schema rawjson.Message) model.ToolInput {
	input, err := model.AdvertisedToolInputFromSchema(schema)
	if err != nil {
		panic(err)
	}
	return input
}

func TestRemoteClientRejectsInvalidRequestBeforeTransport(t *testing.T) {
	transportCalls := 0
	client := requireRemoteClient(t,
		func(context.Context, *model.Request) (*model.Response, error) {
			transportCalls++
			return &model.Response{}, nil
		},
		func(context.Context, *model.Request) (model.Streamer, error) {
			transportCalls++
			return &stubStreamer{}, nil
		},
	)

	response, err := client.Complete(t.Context(), nil)
	require.Nil(t, response)
	require.ErrorContains(t, err, "model request is required")
	stream, err := client.Stream(t.Context(), nil)
	require.Nil(t, stream)
	require.ErrorContains(t, err, "model request is required")
	require.Zero(t, transportCalls)
}

func TestRemoteClientRequiresConfiguredCallbacksBeforeRequestValidation(t *testing.T) {
	client, err := NewRemoteClient(nil, nil)
	require.Nil(t, client)
	require.EqualError(t, err, "gateway: complete callback is required")
}

func TestCountingRemoteClientPreservesTokenCounter(t *testing.T) {
	client, err := NewCountingRemoteClient(
		func(context.Context, *model.Request) (*model.Response, error) {
			return nil, errors.New("unexpected complete call")
		},
		func(context.Context, *model.Request) (model.Streamer, error) {
			return nil, errors.New("unexpected stream call")
		},
		func(_ context.Context, req *model.Request) (model.TokenCount, error) {
			return model.TokenCount{
				Model:       req.Model,
				ModelClass:  req.ModelClass,
				InputTokens: 42,
				Exact:       true,
			}, nil
		},
	)
	require.NoError(t, err)

	count, err := client.CountTokens(t.Context(), &model.Request{
		Model:      "remote-model",
		ModelClass: model.ModelClassDefault,
	})

	require.NoError(t, err)
	require.Equal(t, model.TokenCount{
		Model:       "remote-model",
		ModelClass:  model.ModelClassDefault,
		InputTokens: 42,
		Exact:       true,
	}, count)
}

func TestDirectCountingRemoteProviderValidatesStructuredOutputSchema(t *testing.T) {
	tests := []struct {
		name    string
		schema  rawjson.Message
		wantErr bool
	}{
		{name: "missing", wantErr: true},
		{name: "malformed", schema: rawjson.Message(`{"type":`), wantErr: true},
		{
			name:    "semantically invalid",
			schema:  rawjson.Message(`{"type":"not-a-json-type"}`),
			wantErr: true,
		},
		{name: "valid", schema: rawjson.Message(`{"type":"object"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			countCalls := 0
			raw, err := NewCountingRemoteProvider(
				func(context.Context, *model.Request) (*model.Response, error) {
					return nil, errors.New("unexpected complete call")
				},
				func(context.Context, *model.Request) (model.Streamer, error) {
					return nil, errors.New("unexpected stream call")
				},
				func(context.Context, *model.Request) (model.TokenCount, error) {
					countCalls++
					return model.TokenCount{}, nil
				},
			)
			require.NoError(t, err)
			counter, ok := raw.(model.TokenCounter)
			require.True(t, ok)
			request := &model.Request{
				StructuredOutput: &model.StructuredOutput{Name: "result", Schema: test.schema},
			}

			_, err = counter.CountTokens(t.Context(), request)

			if test.wantErr {
				require.Error(t, err)
				require.Zero(t, countCalls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, countCalls)
		})
	}
}

func TestRemoteClientWithoutCounterReportsUnsupported(t *testing.T) {
	client := requireRemoteClient(t, nil, nil)

	_, err := client.CountTokens(t.Context(), &model.Request{})

	require.ErrorIs(t, err, model.ErrTokenCountingUnsupported)
}

func TestRemoteClientClosesStreamReturnedWithError(t *testing.T) {
	transportErr := errors.New("transport failed")
	closeErr := errors.New("close failed")
	upstream := &stubStreamer{closeErr: closeErr}
	client := requireRemoteClient(t,
		nil,
		func(context.Context, *model.Request) (model.Streamer, error) {
			return upstream, transportErr
		},
	)

	stream, err := client.Stream(t.Context(), &model.Request{})

	require.Nil(t, stream)
	require.ErrorIs(t, err, transportErr)
	require.ErrorIs(t, err, closeErr)
	require.True(t, upstream.closed)
}

func TestRemoteClientKeepsPreTransportRequestContract(t *testing.T) {
	request := &model.Request{}
	client := requireRemoteClient(t,
		func(_ context.Context, request *model.Request) (*model.Response, error) {
			request.Tools = []*model.ToolDefinition{advertisedGatewayTool("late_tool")}
			return gatewayToolResponse("late_tool"), nil
		},
		nil,
	)

	response, err := client.Complete(t.Context(), request)
	require.Nil(t, response)
	require.ErrorContains(t, err, `model returned tool "late_tool" that was not present in its request`)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
}

func TestRemoteClientPreservesTypedTransportOutputFailure(t *testing.T) {
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	transportErr := contract.RejectResponse(nil, errors.New("remote adapter rejected output"))
	client := requireRemoteClient(t,
		func(context.Context, *model.Request) (*model.Response, error) {
			return nil, transportErr
		},
		nil,
	)

	response, err := client.Complete(t.Context(), &model.Request{})
	require.Nil(t, response)
	require.ErrorIs(t, err, transportErr)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
}

func TestRemoteClientStreamKeepsPreTransportRequestContract(t *testing.T) {
	request := &model.Request{}
	client := requireRemoteClient(t,
		nil,
		func(_ context.Context, request *model.Request) (model.Streamer, error) {
			request.Tools = []*model.ToolDefinition{advertisedGatewayTool("late_tool")}
			return &stubStreamer{
				chunks: []model.Chunk{
					model.ToolCallChunk{ToolCall: model.ToolCall{
						ID:      "call-1",
						Name:    "late_tool",
						Payload: rawjson.Message(`{}`),
					}},
				},
				recvErr:  io.EOF,
				response: gatewayToolResponse("late_tool"),
			}, nil
		},
	)

	stream, err := client.Stream(t.Context(), request)
	require.NoError(t, err)
	chunk, err := stream.Recv()
	require.Nil(t, chunk)
	require.ErrorContains(t, err, `model stream returned tool "late_tool" that was not present in its request`)
}

func TestRemoteClientRejectsTypedNilStream(t *testing.T) {
	var upstream *stubStreamer
	client := requireRemoteClient(t,
		nil,
		func(context.Context, *model.Request) (model.Streamer, error) {
			return upstream, nil
		},
	)

	stream, err := client.Stream(t.Context(), &model.Request{})

	require.Nil(t, stream)
	require.ErrorContains(t, err, "typed nil")
}

func requireRemoteClient(
	t *testing.T,
	complete func(context.Context, *model.Request) (*model.Response, error),
	stream func(context.Context, *model.Request) (model.Streamer, error),
) model.Client {
	t.Helper()
	if complete == nil {
		complete = func(context.Context, *model.Request) (*model.Response, error) {
			return nil, errors.New("unexpected complete call")
		}
	}
	if stream == nil {
		stream = func(context.Context, *model.Request) (model.Streamer, error) {
			return nil, errors.New("unexpected stream call")
		}
	}
	client, err := NewRemoteClient(complete, stream)
	require.NoError(t, err)
	return client
}

func advertisedGatewayTool(name string) *model.ToolDefinition {
	return &model.ToolDefinition{
		Name:  name,
		Input: mustGatewayToolInput(rawjson.Message(`{"type":"object"}`)),
	}
}

func gatewayToolResponse(name string) *model.Response {
	return &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "call-1",
				Name:  name,
				Input: rawjson.Message(`{}`),
			}},
		}},
		StopReason: "tool_use",
	}
}
