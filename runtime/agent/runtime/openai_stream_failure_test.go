package runtime

// This test carries a real decoded provider failure through planner publication
// and Temporal serialization. Retry metadata does not accept a partial response.
import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	openaimodel "goa.design/goa-ai/features/model/openai"
	"goa.design/goa-ai/runtime/agent/internal/modelcall"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestOpenAIStreamFailurePreservesPublishedTextAndTemporalMetadata(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := fmt.Fprint(w, "data: "+`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Visible text","logprobs":[]}`+"\n\n"+
			"data: "+`{"error":{"type":"server_error","code":"internal_server_error","message":"Synthetic failure."}}`+"\n\n")
		assert.NoError(t, err)
	}))
	defer server.Close()
	sdk := openaisdk.NewClient(option.WithAPIKey("synthetic"), option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	client, err := openaimodel.New(openaimodel.Options{Client: &sdk.Responses, DefaultModel: "synthetic-model"})
	require.NoError(t, err)
	stream, err := client.Stream(t.Context(), &model.Request{Messages: []*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Answer briefly."}},
	}}})
	require.NoError(t, err)
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime: runtimeWithModelOutputSink(t, sink),
		runID:   "run-1", sessionID: "session-1", responseID: testPublicationBatchID,
	}
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))
	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.NoError(t, journal.recordModelChunk(t.Context(), invocation, chunk))
	_, streamErr := stream.Recv()
	var original *ssestream.StreamError
	require.ErrorAs(t, streamErr, &original)
	require.NoError(t, stream.Close())
	require.Nil(t, stream.Response())
	require.NoError(t, journal.finalizeModelInvocation(invocation, modelcall.Outcome{
		ProviderCall: modelcall.Result{Called: true, Err: streamErr},
	}))
	activity := &plannerActivityInvocation{
		publicationBatchID: testPublicationBatchID, invocations: journal,
		events: newPlannerEvents("svc.agent", "run-1", "session-1"),
	}
	output, err := activity.failureOutput(t.Context(), activity.planningError(streamErr))
	require.NoError(t, err)
	assert.Equal(t, "Visible text", output.PublishedAssistantText)
	require.NotNil(t, output.PlanningFailure)
	assert.Equal(t, string(model.ProviderErrorKindUnavailable), output.PlanningFailure.Kind)
	assert.True(t, output.PlanningFailure.Retryable)
	assert.Len(t, sink.snapshot(), 1)
	assert.EqualValues(t, 1, requests.Load())

	// Temporal carries the established provider fields, not a native SDK error
	// object. Its retry flag describes the error, not permission to replay text.
	converter := temporal.GetDefaultFailureConverter()
	failure := converter.ErrorToFailure(temporalerrors.Wrap(streamErr))
	encoded, err := proto.Marshal(failure)
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(encoded, failure))
	decoded := converter.FailureToError(failure)
	var app *temporal.ApplicationError
	require.ErrorAs(t, decoded, &app)
	assert.False(t, app.NonRetryable())
	pe, ok := temporalerrors.Provider(decoded)
	require.True(t, ok)
	assert.Equal(t, "internal_server_error", pe.Code())
	assert.Equal(t, "Synthetic failure.", pe.Message())
	assert.Equal(t, model.ProviderErrorKindUnavailable, pe.Kind())
	assert.True(t, pe.Retryable())
	assert.Zero(t, pe.HTTPStatus())
	assert.Empty(t, pe.RequestID())
}

func TestOpenAIStreamMalformedMessageTemporalMetadata(t *testing.T) {
	for _, message := range []string{`123`, `{"unexpected":"object"}`} {
		t.Run(message, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, err := fmt.Fprint(w, `data: {"error":{"type":"server_error","code":"internal_server_error","message":`+message+"}}\n\n")
				assert.NoError(t, err)
			}))
			defer server.Close()
			sdk := openaisdk.NewClient(option.WithAPIKey("synthetic"), option.WithBaseURL(server.URL), option.WithMaxRetries(0))
			client, err := openaimodel.New(openaimodel.Options{Client: &sdk.Responses, DefaultModel: "synthetic-model"})
			require.NoError(t, err)
			stream, err := client.Stream(t.Context(), &model.Request{Messages: []*model.Message{{
				Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Answer briefly."}},
			}}})
			require.NoError(t, err)
			_, streamErr := stream.Recv()
			var original *ssestream.StreamError
			require.ErrorAs(t, streamErr, &original)
			require.NoError(t, stream.Close())
			assert.EqualValues(t, 1, requests.Load())

			converter := temporal.GetDefaultFailureConverter()
			failure := converter.ErrorToFailure(temporalerrors.Wrap(streamErr))
			encoded, err := proto.Marshal(failure)
			require.NoError(t, err)
			require.NoError(t, proto.Unmarshal(encoded, failure))
			decoded := converter.FailureToError(failure)
			var app *temporal.ApplicationError
			require.ErrorAs(t, decoded, &app)
			assert.True(t, app.NonRetryable())
			pe, ok := temporalerrors.Provider(decoded)
			require.True(t, ok)
			assert.Equal(t, model.ProviderErrorKindUnknown, pe.Kind())
			assert.False(t, pe.Retryable())
			assert.Empty(t, pe.Code())
			assert.Equal(t, original.Error(), pe.Message())
			assert.Zero(t, pe.HTTPStatus())
			assert.Empty(t, pe.RequestID())
		})
	}
}
