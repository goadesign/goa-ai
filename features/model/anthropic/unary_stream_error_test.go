// These tests verify that Anthropic completions using the streaming transport
// retain receive errors unchanged after successful cleanup. A failed cleanup
// remains a separate cause, and only literal EOF permits a completed response.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/features/model/gateway"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// unaryResultStream emits one chunk before its terminal receive error and
	// records which completion and cleanup methods the caller uses.
	unaryResultStream struct {
		recvErr       error
		closeErr      error
		response      *model.Response
		recvCalls     int
		responseCalls int
		closeCalls    int
	}

	// unaryResponseBody records cleanup of the HTTP body decoded by the SDK.
	unaryResponseBody struct {
		io.Reader
		closeCalls int
	}
)

func TestCompleteFromAnthropicStreamPreservesErrors(t *testing.T) {
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	rejection := contract.RejectProviderOutput(
		model.OutputValidationToolIdentity,
		nil,
		model.NewUnadvertisedToolNameError("catalog_missing"),
	)
	providerErr := model.NewProviderError("anthropic", "stream_recv", 503,
		model.ProviderErrorKindUnavailable, "overloaded", "provider unavailable", "", true, nil)
	closeErr := errors.New("response body close failed")
	wrappedEOF := fmt.Errorf("connection ended before completion: %w", io.EOF)
	wrappedRejection := fmt.Errorf("provider context: %w", rejection)
	tests := []struct {
		name     string
		recvErr  error
		closeErr error
		complete bool
	}{
		{name: "output rejection", recvErr: rejection},
		{name: "wrapped output rejection", recvErr: wrappedRejection},
		{name: "provider failure", recvErr: providerErr},
		{name: "cancellation", recvErr: context.Canceled},
		{name: "deadline", recvErr: context.DeadlineExceeded},
		{name: "wrapped EOF is failure", recvErr: wrappedEOF},
		{name: "rejection and cleanup failure", recvErr: rejection, closeErr: closeErr},
		{name: "provider and cleanup failure", recvErr: providerErr, closeErr: closeErr},
		{name: "cancellation and cleanup failure", recvErr: context.Canceled, closeErr: closeErr},
		{name: "wrapped EOF and cleanup failure", recvErr: wrappedEOF, closeErr: closeErr},
		{name: "literal EOF succeeds", recvErr: io.EOF, complete: true},
		{name: "literal EOF and cleanup failure", recvErr: io.EOF, closeErr: closeErr, complete: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &unaryResultStream{
				recvErr:  test.recvErr,
				closeErr: test.closeErr,
				response: &model.Response{StopReason: "end_turn"},
			}

			response, err := completeFromAnthropicStream(stream)

			assert.Equal(t, 2, stream.recvCalls)
			assert.Equal(t, 1, stream.closeCalls)
			if test.complete {
				assert.Equal(t, 1, stream.responseCalls)
				if test.closeErr == nil {
					require.NoError(t, err)
					assert.Same(t, stream.response, response)
				} else {
					assert.Same(t, test.closeErr, err)
					assert.Nil(t, response)
				}
				return
			}
			assert.Zero(t, stream.responseCalls)
			assert.Nil(t, response)
			if test.closeErr == nil {
				//nolint:errorlint // Preserve the exact error, including value-type sentinels.
				if err != test.recvErr {
					t.Fatalf("expected original %T, got %T", test.recvErr, err)
				}
				return
			}
			require.ErrorIs(t, err, test.recvErr)
			require.ErrorIs(t, err, test.closeErr)
			assert.EqualError(t, err, test.recvErr.Error()+"\n"+test.closeErr.Error())
		})
	}
}

func TestGatewayCompleteAnthropicStreamPreservesOutputRejection(t *testing.T) {
	const events = `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-sonnet-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_test","name":"catalog_list_nearby","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`
	body := &unaryResponseBody{Reader: strings.NewReader(events)}
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		var request struct {
			Stream    bool `json:"stream"`
			MaxTokens int  `json:"max_tokens"`
			Tools     []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&request))
		assert.True(t, request.Stream)
		assert.Equal(t, 32768, request.MaxTokens)
		require.Len(t, request.Tools, 1)
		assert.Equal(t, "catalog_list_items", request.Tools[0].Name)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}, nil
	})}
	sdkClient := sdk.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL("https://anthropic.test"),
		option.WithHTTPClient(httpClient),
	)
	raw, err := NewProvider(&sdkClient.Messages, Options{DefaultModel: "claude-sonnet-5"})
	require.NoError(t, err)
	server, err := gateway.NewServer(gateway.WithProvider(raw))
	require.NoError(t, err)

	response, err := server.Complete(t.Context(), &model.Request{
		MaxTokens:  32768,
		ModelClass: model.ModelClassDefault,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "List catalog items."}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "catalog.list_items",
			Description: "List catalog items.",
			Input:       mustAnthropicToolInput(t, rawjson.Message(`{"type":"object"}`)),
		}},
	})

	assert.Nil(t, response)
	assert.Equal(t, 1, requests)
	assert.Equal(t, 1, body.closeCalls)
	// The raw gateway must return the validation error itself, not a wrapper
	// that would make a transport classify this as a separate internal failure.
	//nolint:errorlint // This regression requires direct error identity.
	rejected, ok := err.(*model.OutputValidationError)
	require.True(t, ok, "expected direct OutputValidationError, got %T: %v", err, err)
	assert.Equal(t, model.OutputValidationToolIdentity, rejected.Kind())
	assert.Empty(t, rejected.RecoveryCorrection())
	cause := errors.Unwrap(rejected)
	require.EqualError(t, cause, "anthropic stream: translate tool use: model returned an unadvertised tool name")
	name, marked := model.UnadvertisedToolName(cause)
	assert.True(t, marked)
	assert.Equal(t, "catalog_list_nearby", name)
	assert.Equal(t, &model.TokenUsage{
		Model:        "claude-sonnet-5",
		ModelClass:   model.ModelClassDefault,
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
	}, rejected.Usage())
}

func (s *unaryResultStream) Recv() (model.Chunk, error) {
	s.recvCalls++
	if s.recvCalls == 1 {
		return model.UsageChunk{}, nil
	}
	return nil, s.recvErr
}

func (s *unaryResultStream) Response() *model.Response {
	s.responseCalls++
	return s.response
}

func (s *unaryResultStream) Close() error {
	s.closeCalls++
	return s.closeErr
}

func (b *unaryResponseBody) Close() error {
	b.closeCalls++
	return nil
}
