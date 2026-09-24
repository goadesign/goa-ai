package openai

// These tests send synthetic HTTP event streams through the official SDK and
// adapter. Error metadata must not replay requests or publish incomplete tools.
import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

type failingCloseBody struct {
	io.Reader
	err error
}

func TestNestedStreamErrorMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, payload, code string
		kind                model.ProviderErrorKind
		retryable           bool
	}{
		{"server", `{"error":{"type":"server_error","code":"internal_server_error","message":"Synthetic failure."}}`, "internal_server_error", model.ProviderErrorKindUnavailable, true},
		{"conflicting_type", `{"error":{"type":"invalid_request_error","code":"internal_server_error","message":"Synthetic failure."}}`, "internal_server_error", model.ProviderErrorKindUnknown, false},
		{"missing_type", `{"error":{"code":"internal_server_error","message":"Synthetic failure."}}`, "internal_server_error", model.ProviderErrorKindUnknown, false},
		{"unknown", `{"error":{"type":"server_error","code":"new_code","message":"Synthetic failure."}}`, "new_code", model.ProviderErrorKindUnknown, false},
		{"invalid", `{"error":{"type":"invalid_request_error","code":"invalid_prompt","message":"Synthetic failure."}}`, "invalid_prompt", model.ProviderErrorKindInvalidRequest, false},
		{"auth", `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"Synthetic failure."}}`, "invalid_api_key", model.ProviderErrorKindInvalidRequest, false},
		{"rate_limit", `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Synthetic failure."}}`, "rate_limit_exceeded", model.ProviderErrorKindRateLimited, true},
		{"existing_server_code", `{"error":{"code":"server_error","message":"Synthetic failure."}}`, "server_error", model.ProviderErrorKindUnavailable, true},
		{"malformed_shape", `{"error":"Synthetic failure."}`, "", model.ProviderErrorKindUnknown, false},
		{"malformed_json", `{"error":{"type":"server_error","code":"internal_server_error","message":"Synthetic failure."}`, "", model.ProviderErrorKindUnknown, false},
		{"type_case", `{"error":{"type":"SERVER_ERROR","code":"internal_server_error","message":"Synthetic failure."}}`, "internal_server_error", model.ProviderErrorKindUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, err := fmt.Fprintf(w, "data: %s\n\n", tc.payload)
				assert.NoError(t, err)
			}))
			defer server.Close()
			sdk := openaisdk.NewClient(option.WithAPIKey("synthetic"), option.WithBaseURL(server.URL), option.WithMaxRetries(0))
			client, err := New(Options{Client: &sdk.Responses, DefaultModel: "synthetic-model"})
			require.NoError(t, err)
			stream, err := client.Stream(t.Context(), replayTestRequest(""))
			require.NoError(t, err)
			_, err = stream.Recv()
			var pe *model.ProviderError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, tc.kind, pe.Kind())
			assert.Equal(t, tc.retryable, pe.Retryable())
			assert.Equal(t, tc.code, pe.Code())
			assert.Zero(t, pe.HTTPStatus())
			assert.Empty(t, pe.RequestID())
			assert.Equal(t, "responses.stream", pe.Operation())
			if tc.code != "" {
				assert.Equal(t, "Synthetic failure.", pe.Message())
			}
			var original *ssestream.StreamError
			require.ErrorAs(t, err, &original)
			assert.Equal(t, tc.payload+"\n", string(original.Event.Data))
			require.NoError(t, stream.Close())
			assert.EqualValues(t, 1, requests.Load())
		})
	}
}

func TestNestedStreamErrorPreservesPartialOutputWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		want         []string
	}{
		{"empty", "", nil},
		{"text", `data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Partial text","logprobs":[]}` + "\n\n", []string{model.ChunkTypeText}},
		{"thinking", `data: {"type":"response.reasoning_summary_text.delta","sequence_number":1,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"Partial reasoning"}` + "\n\n", []string{model.ChunkTypeThinking}},
		{"tool", `data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"","status":"in_progress"}}` + "\n\n" +
			`data: {"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"{\"id\":"}` + "\n\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, err := fmt.Fprint(w, tc.prefix+"data: "+`{"error":{"type":"server_error","code":"internal_server_error","message":"Synthetic failure."}}`+"\n\n")
				assert.NoError(t, err)
			}))
			defer server.Close()
			sdk := openaisdk.NewClient(option.WithAPIKey("synthetic"), option.WithBaseURL(server.URL), option.WithMaxRetries(0))
			client, err := New(Options{Client: &sdk.Responses, DefaultModel: "synthetic-model"})
			require.NoError(t, err)
			stream, err := client.Stream(t.Context(), openAIToolRequest())
			require.NoError(t, err)
			var kinds []string
			for {
				chunk, recvErr := stream.Recv()
				if recvErr != nil {
					var pe *model.ProviderError
					require.ErrorAs(t, recvErr, &pe)
					assert.True(t, pe.Retryable())
					var original *ssestream.StreamError
					require.ErrorAs(t, recvErr, &original)
					break
				}
				kinds = append(kinds, chunk.Kind())
			}
			assert.Equal(t, tc.want, kinds)
			assert.NotContains(t, kinds, model.ChunkTypeToolCall)
			assert.Nil(t, stream.Response())
			require.NoError(t, stream.Close())
			assert.EqualValues(t, 1, requests.Load())
		})
	}
}

func TestStreamErrorPreservesCancellationAndFlatClassification(t *testing.T) {
	for _, canceled := range []error{context.Canceled, context.DeadlineExceeded} {
		err := errors.Join(canceled, &ssestream.StreamError{Event: ssestream.Event{Data: []byte(`{"error":{"type":"server_error","code":"internal_server_error"}}`)}})
		assert.Same(t, err, wrapOpenAIError("responses.stream", err))
	}
	for _, tc := range []struct {
		code      string
		kind      model.ProviderErrorKind
		retryable bool
	}{
		{"internal_server_error", model.ProviderErrorKindUnknown, false},
		{"server_error", model.ProviderErrorKindUnavailable, true},
		{"rate_limit_exceeded", model.ProviderErrorKindRateLimited, true},
		{"invalid_prompt", model.ProviderErrorKindInvalidRequest, false},
	} {
		var pe *model.ProviderError
		require.ErrorAs(t, providerErrorFromResponseFailure("responses.stream", tc.code, "Synthetic failure.", nil), &pe)
		assert.Equal(t, tc.kind, pe.Kind())
		assert.Equal(t, tc.retryable, pe.Retryable())
	}
}

func TestStreamErrorChangePreservesHTTPClassification(t *testing.T) {
	for _, tc := range []struct {
		status    int
		kind      model.ProviderErrorKind
		retryable bool
	}{
		{400, model.ProviderErrorKindInvalidRequest, false},
		{401, model.ProviderErrorKindAuth, false},
		{403, model.ProviderErrorKindAuth, false},
		{429, model.ProviderErrorKindRateLimited, true},
		{500, model.ProviderErrorKindUnavailable, true},
		{503, model.ProviderErrorKindUnavailable, true},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			original := &openaisdk.Error{StatusCode: tc.status, Code: "internal_server_error", Message: "Synthetic failure."}
			var pe *model.ProviderError
			require.ErrorAs(t, wrapOpenAIError("responses.stream", original), &pe)
			assert.Equal(t, tc.kind, pe.Kind())
			assert.Equal(t, tc.retryable, pe.Retryable())
			assert.Equal(t, tc.status, pe.HTTPStatus())
			assert.ErrorIs(t, pe, original)
		})
	}
}

func TestNestedStreamErrorPreservesCloseFailure(t *testing.T) {
	for _, bedrock := range []bool{false, true} {
		t.Run(fmt.Sprintf("bedrock_%t", bedrock), func(t *testing.T) {
			closeErr := errors.New("synthetic close failure")
			var requests atomic.Int32
			client := newReplayTestClient(t, bedrock, Options{}, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body: &failingCloseBody{
						Reader: strings.NewReader("data: " + `{"error":{"type":"server_error","code":"internal_server_error","message":"Synthetic failure."}}` + "\n\n"),
						err:    closeErr,
					},
				}, nil
			}))
			stream, err := client.Stream(t.Context(), replayTestRequest(""))
			require.NoError(t, err)
			_, err = stream.Recv()
			var pe *model.ProviderError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, model.ProviderErrorKindUnavailable, pe.Kind())
			var original *ssestream.StreamError
			require.ErrorAs(t, err, &original)
			require.ErrorIs(t, stream.Close(), closeErr)
			assert.EqualValues(t, 1, requests.Load())
		})
	}
}

func (b *failingCloseBody) Close() error {
	return b.err
}
