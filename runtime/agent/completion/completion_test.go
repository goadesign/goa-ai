package completion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

const outputLimitStopReason = "max_tokens"

// mustCompletionToolInput compiles a static test schema.
func mustCompletionToolInput(t *testing.T, schema rawjson.Message) model.ToolInput {
	t.Helper()
	input, err := model.AdvertisedToolInputFromSchema(schema)
	require.NoError(t, err)
	return input
}

type testCompletionResult struct {
	AssistantText string `json:"assistant_text"`
}

func testCompletionSpec() Spec[testCompletionResult] {
	return Spec[testCompletionResult]{
		Name:                     "draft_from_transcript",
		Description:              "Synthesize task draft",
		Schema:                   rawjson.Message(`{"type":"object","required":["assistant_text"]}`),
		SchemaWithoutRootExample: rawjson.Message(`{"type":"object","required":["assistant_text"]}`),
		ExampleJSON:              rawjson.Message(`{"assistant_text":"Created a draft."}`),
		Codec: tools.JSONCodec[testCompletionResult]{
			ToJSON:   marshalTestCompletionResult,
			FromJSON: unmarshalTestCompletionResult,
		},
	}
}

func marshalTestCompletionResult(value testCompletionResult) ([]byte, error) {
	return json.Marshal(value)
}

func unmarshalTestCompletionResult(data []byte) (testCompletionResult, error) {
	var out testCompletionResult
	err := json.Unmarshal(data, &out)
	return out, err
}

// requireCompletionResponseEqual compares every caller-visible response field
// while ignoring the private ownership marker added by model validation.
func requireCompletionResponseEqual(t *testing.T, expected, actual *model.Response) {
	t.Helper()
	require.NotNil(t, expected)
	require.NotNil(t, actual)
	require.Equal(t, expected.Usage, actual.Usage)
	require.Equal(t, expected.StopReason, actual.StopReason)
	require.Equal(t, expected.OutputLimited, actual.OutputLimited)
	require.Len(t, actual.Content, len(expected.Content))
	for index := range expected.Content {
		require.Equal(t, expected.Content[index].Role, actual.Content[index].Role)
		require.Equal(t, expected.Content[index].Parts, actual.Content[index].Parts)
		require.Equal(t, expected.Content[index].Meta, actual.Content[index].Meta)
	}
}

// completionOutputContractCause returns the diagnostic cause kept behind the
// privacy-safe error text so tests can verify why completion rejected output.
func completionOutputContractCause(t *testing.T, err error) error {
	t.Helper()
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	cause := errors.Unwrap(outputErr)
	require.Error(t, cause)
	var validationErr *model.OutputValidationError
	if errors.As(cause, &validationErr) {
		cause = errors.Unwrap(validationErr)
		require.Error(t, cause)
	}
	return cause
}

type recordingCompletionClient struct {
	request   *model.Request
	requests  []*model.Request
	response  *model.Response
	responses []*model.Response
	streamer  model.Streamer
	err       error
	streamErr error
}

type validatingCompletionClient struct {
	validated *model.Response
	returned  *model.Response
	mutate    func(*model.Response)
}

type streamResultCompletionClient struct {
	stream model.Streamer
	err    error
}

type forgedCompletionClient struct {
	model.Client
}

type mutatingCompletionObserverProvider struct {
	model.Provider
	replacement string
}

type mutatingCompletionObserverCall struct {
	replacement string
}

func (c *recordingCompletionClient) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	c.request = req
	c.requests = append(c.requests, req)
	if len(c.responses) >= len(c.requests) {
		return c.responses[len(c.requests)-1], nil
	}
	return c.response, c.err
}

func (c *recordingCompletionClient) Stream(_ context.Context, req *model.Request) (model.Streamer, error) {
	c.request = req
	return c.streamer, c.streamErr
}

func (c *validatingCompletionClient) Complete(
	_ context.Context,
	request *model.Request,
) (*model.Response, error) {
	if c.mutate != nil {
		c.mutate(c.returned)
	}
	return c.returned, nil
}

func (*validatingCompletionClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	panic("unexpected streaming completion")
}

func (streamResultCompletionClient) Complete(context.Context, *model.Request) (*model.Response, error) {
	panic("unexpected unary completion")
}

func (c streamResultCompletionClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return c.stream, c.err
}

func (p *mutatingCompletionObserverProvider) PrepareClientCall(
	ctx context.Context,
	_ *model.Request,
) (context.Context, model.ClientCallObserver, error) { //nolint:unparam // The observer interface reserves setup failure.
	return ctx, &mutatingCompletionObserverCall{replacement: p.replacement}, nil
}

func (c *mutatingCompletionObserverCall) ObserveClientComplete(response *model.Response, _ error) error {
	if response != nil {
		response.Content[0].Parts[0] = model.TextPart{Text: c.replacement}
	}
	return nil
}

func (*mutatingCompletionObserverCall) ObserveClientStream(error) (model.StreamObserver, error) {
	return nil, nil
}

func (*mutatingCompletionObserverCall) Finish(error) error {
	return nil
}

func (*mutatingCompletionObserverCall) Abort(error) error {
	return nil
}

func mustCompletionClient(t *testing.T, provider model.Provider) model.Client {
	t.Helper()
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	return client
}

type stubStreamer struct{}

func (stubStreamer) Recv() (model.Chunk, error) {
	return nil, io.EOF
}

func (stubStreamer) Close() error {
	return nil
}

func (stubStreamer) Response() *model.Response {
	return nil
}

type recvResult struct {
	chunk model.Chunk
	err   error
}

type scriptedStreamer struct {
	results  []recvResult
	response *model.Response
	index    int
	closed   bool
	closeErr error
}

type blockingCompletionCloseStreamer struct {
	*scriptedStreamer
	closeStarted chan struct{}
	closeRelease chan struct{}
}

func (s *scriptedStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.results) {
		return nil, io.EOF
	}
	result := s.results[s.index]
	s.index++
	return result.chunk, result.err
}

func (s *scriptedStreamer) Close() error {
	s.closed = true
	return s.closeErr
}

func (s *scriptedStreamer) Response() *model.Response {
	return s.response
}

func (s *blockingCompletionCloseStreamer) Close() error {
	close(s.closeStarted)
	<-s.closeRelease
	return s.scriptedStreamer.Close()
}

type reusingCompletionStreamer struct {
	payload  []byte
	response *model.Response
	step     int
}

func (s *reusingCompletionStreamer) Recv() (model.Chunk, error) {
	switch s.step {
	case 0:
		s.step++
		return model.CompletionChunk{Completion: model.Completion{
			Name:    "draft_from_transcript",
			Payload: s.payload,
		}}, nil
	case 1:
		s.step++
		s.payload[19] = 'X'
		return model.StopChunk{Reason: "stop"}, nil
	default:
		return nil, io.EOF
	}
}

func (s *reusingCompletionStreamer) Close() error {
	return nil
}

func (s *reusingCompletionStreamer) Response() *model.Response {
	return s.response
}

func completionResponse(body string) *model.Response {
	return &model.Response{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: body}},
		}},
		StopReason: "stop",
	}
}

func TestCompleteSetsStructuredOutputAndDecodesTypedValue(t *testing.T) {
	spec := testCompletionSpec()
	client := &recordingCompletionClient{
		response: &model.Response{
			Content: []model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{Text: "internal", Final: true},
					model.TextPart{Text: `{"assistant_text":"created a draft"}`},
				},
			}},
			StopReason: "stop",
		},
	}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "create a task"}},
		}},
	}

	resp, err := Complete(context.Background(), mustCompletionClient(t, client), req, spec)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, testCompletionResult{AssistantText: "created a draft"}, resp.Value)
	assert.NotSame(t, client.response, resp.ModelResponse)
	assert.Equal(t, client.response.StopReason, resp.ModelResponse.StopReason)
	assert.Equal(t, client.response.Usage, resp.ModelResponse.Usage)
	assert.Equal(t, client.response.Content[0].Parts, resp.ModelResponse.Content[0].Parts)
	require.NotNil(t, client.request)
	require.NotNil(t, client.request.StructuredOutput)
	assert.Equal(t, "draft_from_transcript", client.request.StructuredOutput.Name)
	assert.JSONEq(t, `{"type":"object","required":["assistant_text"]}`, string(client.request.StructuredOutput.Schema))
	assert.JSONEq(
		t,
		`{"type":"object","required":["assistant_text"]}`,
		string(client.request.StructuredOutput.SchemaWithoutRootExample),
	)
	assert.JSONEq(t, `{"assistant_text":"Created a draft."}`, string(client.request.StructuredOutput.ExampleJSON))
	assert.Nil(t, req.StructuredOutput)
}
func TestCompleteRejectsOutputLimitedResponse(t *testing.T) {
	rejectedResponse := completionResponse(`{"assistant_text":"truncated but schema valid"}`)
	rejectedResponse.StopReason = outputLimitStopReason
	rejectedResponse.OutputLimited = true
	rejectedResponse.Usage = model.TokenUsage{
		Model:            "provider-model",
		InputTokens:      4,
		OutputTokens:     8,
		TotalTokens:      12,
		CacheReadTokens:  2,
		CacheWriteTokens: 1,
	}
	expectedEvidence := model.EvidenceForResponse(rejectedResponse)

	response, err := Complete(
		t.Context(),
		mustCompletionClient(t, &recordingCompletionClient{response: rejectedResponse}),
		&model.Request{},
		testCompletionSpec(),
	)

	require.Nil(t, response)
	require.ErrorIs(t, err, errOutputLimited)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Equal(t, planner.OutputContractOriginModel, outputErr.Origin())
	require.Empty(t, outputErr.Correction())
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, expectedEvidence, validationErr.Evidence())
	require.Equal(t, &rejectedResponse.Usage, validationErr.Usage())
	require.Empty(t, validationErr.RecoveryCorrection())
	retained, retainErr := validationErr.RejectedResponse()
	require.NoError(t, retainErr)
	requireCompletionResponseEqual(t, rejectedResponse, retained)
}

func TestCompletePreservesMalformedOutputPrecedenceWhenOutputLimited(t *testing.T) {
	rejectedResponse := completionResponse(`{"assistant_text":`)
	rejectedResponse.StopReason = outputLimitStopReason
	rejectedResponse.OutputLimited = true

	response, err := Complete(
		t.Context(),
		mustCompletionClient(t, &recordingCompletionClient{response: rejectedResponse}),
		&model.Request{},
		testCompletionSpec(),
	)

	require.Nil(t, response)
	require.ErrorContains(t, completionOutputContractCause(t, err), "structured output response does not match its schema")
	require.NotErrorIs(t, err, errOutputLimited)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	retained, retainErr := validationErr.RejectedResponse()
	require.NoError(t, retainErr)
	requireCompletionResponseEqual(t, rejectedResponse, retained)
}

func TestCompleteObserverMutationCannotBreakModelResponseValueAgreement(t *testing.T) {
	const original = `{"assistant_text":"canonical"}`
	raw := &mutatingCompletionObserverProvider{
		Provider:    &recordingCompletionClient{response: completionResponse(original)},
		replacement: `{"assistant_text":"mutated by inner observer"}`,
	}
	client, err := model.NewClient(raw)
	require.NoError(t, err)
	client, err = model.WrapClient(client, func(provider model.Provider) model.Provider {
		return &mutatingCompletionObserverProvider{
			Provider:    provider,
			replacement: `{"assistant_text":"mutated by outer observer"}`,
		}
	})
	require.NoError(t, err)

	response, err := Complete(t.Context(), client, &model.Request{}, testCompletionSpec())

	require.NoError(t, err)
	require.Equal(t, testCompletionResult{AssistantText: "canonical"}, response.Value)
	require.Equal(t, model.TextPart{Text: original}, response.ModelResponse.Content[0].Parts[0])
}

func TestCompleteRejectsTypedNilClient(t *testing.T) {
	var client *forgedCompletionClient

	response, err := Complete(t.Context(), client, &model.Request{}, testCompletionSpec())

	require.Nil(t, response)
	require.ErrorContains(t, err, "model client is required")
}

func TestCompleteRejectsCodecFailureWithoutAnotherRequest(t *testing.T) {
	spec := testCompletionSpec()
	spec.Codec.FromJSON = func(data []byte) (testCompletionResult, error) {
		return testCompletionResult{}, tools.NewValidationError(
			"assistant_text must be a string",
			[]*tools.FieldIssue{{
				Field:            "assistant_text",
				Constraint:       "invalid_field_type",
				ExpectedJSONType: "string",
				ActualJSONType:   "number",
			}},
			nil,
		)
	}
	rejectedResponse := completionResponse(`{"assistant_text":42}`)
	rejectedResponse.Usage = model.TokenUsage{TotalTokens: 10}
	client := &recordingCompletionClient{
		responses: []*model.Response{rejectedResponse},
	}
	req := &model.Request{Messages: []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "create a task"}},
	}}}

	response, err := Complete(context.Background(), mustCompletionClient(t, client), req, spec)

	require.Error(t, err)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.ErrorContains(t, completionOutputContractCause(t, err), "assistant_text must be a string")
	assert.Nil(t, response)
	assert.Len(t, client.requests, 1)
	require.Len(t, req.Messages, 1)
	assert.Nil(t, req.StructuredOutput)
}

func TestCompleteValidatesDifferentResponseReturnedByInnerClient(t *testing.T) {
	client := &validatingCompletionClient{
		validated: completionResponse(`{"assistant_text":"valid"}`),
		returned:  completionResponse(`{"assistant_text":42}`),
	}

	response, err := Complete(
		context.Background(),
		mustCompletionClient(t, client),
		&model.Request{},
		testCompletionSpec(),
	)

	require.Nil(t, response)
	require.ErrorContains(t, completionOutputContractCause(t, err), "cannot unmarshal number")
}

func TestCompleteRevalidatesSameResponseAfterInnerClientMutation(t *testing.T) {
	response := completionResponse(`{"assistant_text":"valid"}`)
	client := &validatingCompletionClient{
		validated: response,
		returned:  response,
		mutate: func(response *model.Response) {
			response.Content[0].Parts[0] = model.TextPart{Text: `{"assistant_text":42}`}
		},
	}

	result, err := Complete(
		t.Context(),
		mustCompletionClient(t, client),
		&model.Request{},
		testCompletionSpec(),
	)

	require.Nil(t, result)
	require.ErrorContains(t, completionOutputContractCause(t, err), "cannot unmarshal number")
}

func TestCompleteReturnsProviderAndResponseShapeFailuresAfterOneCall(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "provider error", err: assert.AnError},
		{name: "cancellation", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingCompletionClient{err: test.err}
			response, err := Complete(context.Background(), mustCompletionClient(t, client), &model.Request{
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "create a task"}},
				}},
			}, testCompletionSpec())
			require.ErrorIs(t, err, test.err)
			require.Equal(t, test.err, err)
			assert.Nil(t, response)
			assert.Len(t, client.requests, 1)
			var outputErr *planner.OutputContractError
			require.NotErrorAs(t, err, &outputErr)
			var validationErr *model.OutputValidationError
			require.NotErrorAs(t, err, &validationErr)
		})
	}

	t.Run("invalid response envelope", func(t *testing.T) {
		client := &recordingCompletionClient{response: &model.Response{
			Content: []model.Message{
				{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: `{}`}}},
				{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: `{}`}}},
			},
			StopReason: "stop",
		}}
		response, err := Complete(context.Background(), mustCompletionClient(t, client), &model.Request{
			Messages: []*model.Message{{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "create a task"}},
			}},
		}, testCompletionSpec())
		require.Error(t, err)
		require.ErrorContains(t, completionOutputContractCause(t, err), "requires exactly one assistant message")
		assert.Nil(t, response)
		assert.Len(t, client.requests, 1)
	})
}

func TestCompleteRejectsStreamingRequests(t *testing.T) {
	_, err := Complete(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{}),
		&model.Request{Stream: true},
		testCompletionSpec(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support streaming")
}

func TestCompleteRejectsExampleWithoutSplitSchema(t *testing.T) {
	spec := testCompletionSpec()
	spec.SchemaWithoutRootExample = nil

	_, err := Complete(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{}),
		&model.Request{},
		spec,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "result example requires a schema without the root example")
}

func TestCompleteRejectsToolDefinitions(t *testing.T) {
	_, err := Complete(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{}),
		&model.Request{
			Tools: []*model.ToolDefinition{{
				Name:        "lookup",
				Description: "Search",
				Input:       mustCompletionToolInput(t, rawjson.Message(`{"type":"object"}`)),
			}},
		},
		testCompletionSpec(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not allow tool definitions")
}

func TestCompleteRejectsToolChoice(t *testing.T) {
	_, err := Complete(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{}),
		&model.Request{
			ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeNone},
		},
		testCompletionSpec(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not allow tool choice")
}

func TestStreamSetsStructuredOutputAndEnablesStreaming(t *testing.T) {
	spec := testCompletionSpec()
	client := &recordingCompletionClient{streamer: stubStreamer{}}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "create a task"}},
		}},
	}

	stream, err := Stream(context.Background(), mustCompletionClient(t, client), req, spec)
	require.NoError(t, err)
	require.NotNil(t, stream)
	require.NotNil(t, client.request)
	require.True(t, client.request.Stream)
	require.NotNil(t, client.request.StructuredOutput)
	assert.Equal(t, "draft_from_transcript", client.request.StructuredOutput.Name)
	assert.JSONEq(t, `{"type":"object","required":["assistant_text"]}`, string(client.request.StructuredOutput.Schema))
	assert.False(t, req.Stream)
	assert.Nil(t, req.StructuredOutput)
}

func TestStreamRejectsTypedNilModelStreamer(t *testing.T) {
	var upstream *scriptedStreamer
	client := &recordingCompletionClient{streamer: upstream}

	stream, err := Stream(
		t.Context(),
		mustCompletionClient(t, client),
		&model.Request{},
		testCompletionSpec(),
	)

	require.Nil(t, stream)
	require.ErrorContains(t, completionOutputContractCause(t, err), "typed nil")
}

func TestStreamClosesValidatedModelStreamReturnedWithError(t *testing.T) {
	callErr := errors.New("model stream failed")
	closeErr := errors.New("model stream close failed")
	raw := &scriptedStreamer{closeErr: closeErr}
	request := &model.Request{}
	stream, err := Stream(
		t.Context(),
		mustCompletionClient(t, streamResultCompletionClient{stream: raw, err: callErr}),
		request,
		testCompletionSpec(),
	)

	require.Nil(t, stream)
	require.ErrorIs(t, err, callErr)
	require.ErrorIs(t, err, closeErr)
	require.True(t, raw.closed)
}
func TestStreamPreservesProviderCancellationAndDeadlineErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "provider error", err: assert.AnError},
		{name: "cancellation", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Run("start", func(t *testing.T) {
				stream, err := Stream(
					t.Context(),
					mustCompletionClient(t, &recordingCompletionClient{streamErr: test.err}),
					&model.Request{},
					testCompletionSpec(),
				)

				require.Nil(t, stream)
				require.ErrorIs(t, err, test.err)
				require.Equal(t, test.err, err)
				var outputErr *planner.OutputContractError
				require.NotErrorAs(t, err, &outputErr)
				var validationErr *model.OutputValidationError
				require.NotErrorAs(t, err, &validationErr)
			})

			t.Run("receive", func(t *testing.T) {
				stream, err := Stream(
					t.Context(),
					mustCompletionClient(t, &recordingCompletionClient{
						streamer: &scriptedStreamer{
							response: completionResponse(`{"assistant_text":"not accepted"}`),
							results: []recvResult{
								{chunk: model.CompletionChunk{Completion: model.Completion{
									Name:    "draft_from_transcript",
									Payload: []byte(`{"assistant_text":"not accepted"}`),
								}}},
								{err: test.err},
							},
						},
					}),
					&model.Request{},
					testCompletionSpec(),
				)
				require.NoError(t, err)

				final, err := stream.Recv()

				require.Nil(t, final)
				require.ErrorIs(t, err, test.err)
				var outputErr *planner.OutputContractError
				require.NotErrorAs(t, err, &outputErr)
				var validationErr *model.OutputValidationError
				require.NotErrorAs(t, err, &validationErr)
				require.Nil(t, stream.Response())
				_, ok := stream.Value()
				require.False(t, ok)
			})
		})
	}
}

func TestStreamRejectsInvariantViolations(t *testing.T) {
	cases := []struct {
		name string
		req  *model.Request
		want string
	}{
		{
			name: "structured output override",
			req: &model.Request{
				StructuredOutput: &model.StructuredOutput{
					Name:   "other",
					Schema: tools.RawJSON(`{"type":"object"}`),
				},
			},
			want: "cannot override an existing structured output request",
		},
		{
			name: "tool definitions",
			req: &model.Request{
				Tools: []*model.ToolDefinition{{
					Name:        "lookup",
					Description: "Search",
					Input:       mustCompletionToolInput(t, rawjson.Message(`{"type":"object"}`)),
				}},
			},
			want: "does not allow tool definitions",
		},
		{
			name: "tool choice",
			req: &model.Request{
				ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeNone},
			},
			want: "does not allow tool choice",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Stream(
				context.Background(),
				mustCompletionClient(t, &recordingCompletionClient{}),
				tc.req,
				testCompletionSpec(),
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestStreamEnforcesCanonicalCompletionContract(t *testing.T) {
	spec := testCompletionSpec()
	upstream := &scriptedStreamer{
		response: &model.Response{
			Content: []model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: `{"assistant_text":"created a draft"}`},
				},
			}},
			StopReason: "stop",
		},
		results: []recvResult{
			{
				chunk: model.CompletionDeltaChunk{
					Delta: model.CompletionDelta{
						Name:  "draft_from_transcript",
						Delta: `{"assistant_text":"draft`,
					},
				},
			},
			{
				chunk: model.CompletionChunk{
					Completion: model.Completion{
						Name:    "draft_from_transcript",
						Payload: []byte(`{"assistant_text":"created a draft"}`),
					},
				},
			},
			{
				chunk: model.StopChunk{Reason: "stop"},
			},
			{err: io.EOF},
		},
	}
	stream, err := Stream(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{streamer: upstream}),
		&model.Request{},
		spec,
	)
	require.NoError(t, err)

	_, ok := stream.Value()
	require.False(t, ok)
	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.IsType(t, model.CompletionDeltaChunk{}, chunk)
	_, ok = stream.Value()
	require.False(t, ok)

	chunk, err = stream.Recv()
	require.NoError(t, err)
	require.IsType(t, model.CompletionChunk{}, chunk)
	value, ok := stream.Value()
	require.True(t, ok)
	require.Equal(t, "created a draft", value.AssistantText)
	require.True(t, upstream.closed)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}
func TestStreamRejectsOutputLimitedResponseWithoutFinalCompletion(t *testing.T) {
	closeErr := errors.New("provider close failed")
	rejectedResponse := completionResponse(`{"assistant_text":"truncated but schema valid"}`)
	rejectedResponse.StopReason = outputLimitStopReason
	rejectedResponse.OutputLimited = true
	rejectedResponse.Usage = model.TokenUsage{
		Model:        "provider-model",
		InputTokens:  4,
		OutputTokens: 8,
		TotalTokens:  12,
	}
	expectedEvidence := model.EvidenceForResponse(rejectedResponse)
	upstream := &scriptedStreamer{
		response: rejectedResponse,
		closeErr: closeErr,
		results: []recvResult{
			{chunk: model.CompletionDeltaChunk{Delta: model.CompletionDelta{
				Name:  "draft_from_transcript",
				Delta: `{"assistant_text":"truncated`,
			}}},
			{chunk: model.CompletionChunk{Completion: model.Completion{
				Name:    "draft_from_transcript",
				Payload: []byte(`{"assistant_text":"truncated but schema valid"}`),
			}}},
			{chunk: model.UsageChunk{Usage: rejectedResponse.Usage}},
			{chunk: model.StopChunk{Reason: outputLimitStopReason, OutputLimited: true}},
			{err: io.EOF},
		},
	}
	stream, err := Stream(
		t.Context(),
		mustCompletionClient(t, &recordingCompletionClient{streamer: upstream}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	preview, err := stream.Recv()
	require.NoError(t, err)
	require.IsType(t, model.CompletionDeltaChunk{}, preview)
	_, ok := stream.Value()
	require.False(t, ok)

	final, err := stream.Recv()
	require.Nil(t, final)
	require.ErrorIs(t, err, errOutputLimited)
	require.NotErrorIs(t, err, closeErr)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Equal(t, planner.OutputContractOriginModel, outputErr.Origin())
	require.Empty(t, outputErr.Correction())
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, expectedEvidence, validationErr.Evidence())
	require.Equal(t, &rejectedResponse.Usage, validationErr.Usage())
	require.Empty(t, validationErr.RecoveryCorrection())
	retained, retainErr := validationErr.RejectedResponse()
	require.NoError(t, retainErr)
	requireCompletionResponseEqual(t, rejectedResponse, retained)
	require.Nil(t, stream.Response())
	_, ok = stream.Value()
	require.False(t, ok)

	replayed, replayErr := stream.Recv()
	require.Nil(t, replayed)
	require.Equal(t, err, replayErr)
	_, ok = stream.Value()
	require.False(t, ok)
}

func TestStreamWithholdsTypedValueWhenFinalizationFails(t *testing.T) {
	closeErr := errors.New("provider close failed")
	upstream := &scriptedStreamer{
		response: completionResponse(`{"assistant_text":"created a draft"}`),
		results: []recvResult{
			{chunk: model.CompletionChunk{Completion: model.Completion{
				Name:    "draft_from_transcript",
				Payload: []byte(`{"assistant_text":"created a draft"}`),
			}}},
			{chunk: model.StopChunk{Reason: "stop"}},
			{err: io.EOF},
		},
		closeErr: closeErr,
	}
	stream, err := Stream(
		t.Context(),
		mustCompletionClient(t, &recordingCompletionClient{streamer: upstream}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	chunk, recvErr := stream.Recv()

	require.Nil(t, chunk)
	require.ErrorIs(t, recvErr, closeErr)
	_, ok := stream.Value()
	require.False(t, ok)
	require.True(t, upstream.closed)
	require.ErrorIs(t, stream.Close(), closeErr)
}

func TestStreamRetainsCancellationDuringValidationFinalization(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	closeErr := errors.New("provider close failed")
	raw := &blockingCompletionCloseStreamer{
		scriptedStreamer: &scriptedStreamer{
			response: completionResponse(`{"assistant_text":42}`),
			results: []recvResult{
				{chunk: model.CompletionChunk{Completion: model.Completion{
					Name:    "draft_from_transcript",
					Payload: []byte(`{"assistant_text":42}`),
				}}},
				{chunk: model.StopChunk{Reason: "stop"}},
				{err: io.EOF},
			},
			closeErr: closeErr,
		},
		closeStarted: make(chan struct{}),
		closeRelease: make(chan struct{}),
	}
	stream, err := Stream(
		ctx,
		mustCompletionClient(t, &recordingCompletionClient{streamer: raw}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		_, recvErr := stream.Recv()
		result <- recvErr
	}()
	<-raw.closeStarted

	cancel()
	close(raw.closeRelease)
	recvErr := <-result

	var validationErr *model.OutputValidationError
	require.ErrorAs(t, recvErr, &validationErr)
	require.ErrorIs(t, recvErr, context.Canceled)
	require.ErrorIs(t, recvErr, closeErr)
	_, ok := stream.Value()
	require.False(t, ok)
}

func TestStreamRejectsWrappedEOFAroundCompletion(t *testing.T) {
	completion := model.CompletionChunk{Completion: model.Completion{
		Name:    "draft_from_transcript",
		Payload: []byte(`{"assistant_text":"created a draft"}`),
	}}
	for _, test := range []struct {
		name   string
		chunks []recvResult
	}{
		{
			name: "before completion",
		},
		{
			name:   "after completion",
			chunks: []recvResult{{chunk: completion}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrappedEOF := fmt.Errorf("provider stream failed: %w", io.EOF)
			results := append([]recvResult(nil), test.chunks...)
			results = append(results, recvResult{err: wrappedEOF})
			upstream := &scriptedStreamer{
				results: results,
				response: &model.Response{
					Content: []model.Message{{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: `{"assistant_text":"created a draft"}`}},
					}},
					StopReason: "stop",
				},
			}
			stream, err := Stream(
				t.Context(),
				mustCompletionClient(t, &recordingCompletionClient{streamer: upstream}),
				&model.Request{},
				testCompletionSpec(),
			)
			require.NoError(t, err)

			chunk, err := stream.Recv()

			require.Nil(t, chunk)
			require.Equal(t, wrappedEOF, err)
			_, ok := stream.Value()
			require.False(t, ok)
		})
	}
}

func TestStreamRequiresExactCompletionBytes(t *testing.T) {
	upstream := &scriptedStreamer{
		response: &model.Response{
			Content: []model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: `{"summary":null,"assistant_text":"created a draft"}`}},
			}},
			StopReason: "stop",
		},
		results: []recvResult{
			{chunk: model.CompletionChunk{Completion: model.Completion{
				Name:    "draft_from_transcript",
				Payload: []byte(`{"assistant_text":"created a draft"}`),
			}}},
			{chunk: model.StopChunk{Reason: "stop"}},
			{err: io.EOF},
		},
	}
	stream, err := Stream(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{streamer: upstream}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.ErrorContains(t, completionOutputContractCause(t, err), "stream completion does not match canonical response")
}

func TestStreamRejectsMultipleResponseMessages(t *testing.T) {
	upstream := &scriptedStreamer{
		response: &model.Response{
			Content: []model.Message{
				{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: `{"assistant_text":`}},
				},
				{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: `"created a draft"}`}},
				},
			},
			StopReason: "stop",
		},
		results: []recvResult{
			{chunk: model.CompletionChunk{Completion: model.Completion{
				Name:    "draft_from_transcript",
				Payload: []byte(`{"assistant_text":"created a draft"}`),
			}}},
			{chunk: model.StopChunk{Reason: "stop"}},
			{err: io.EOF},
		},
	}
	stream, err := Stream(
		t.Context(),
		mustCompletionClient(t, &recordingCompletionClient{streamer: upstream}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()

	require.ErrorContains(
		t,
		completionOutputContractCause(t, err),
		"structured output response requires exactly one assistant message, got 2",
	)
	_, ok := stream.Value()
	require.False(t, ok)
}

func TestStreamOwnsFinalPayloadBeforeDraining(t *testing.T) {
	payload := []byte(`{"assistant_text":"created a draft"}`)
	upstream := &reusingCompletionStreamer{
		payload: payload,
		response: &model.Response{
			Content: []model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: `{"assistant_text":"created a draft"}`},
				},
			}},
			StopReason: "stop",
		},
	}
	stream, err := Stream(
		t.Context(),
		mustCompletionClient(t, &recordingCompletionClient{streamer: upstream}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.NoError(t, err)
	value, ok := stream.Value()

	require.True(t, ok)
	require.Equal(t, "created a draft", value.AssistantText)
	require.Equal(t, byte('X'), payload[19])
}

func TestStreamLatchesFirstTerminalError(t *testing.T) {
	upstream := &scriptedStreamer{
		results: []recvResult{
			{chunk: model.TextChunk{Message: model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "not a completion"}},
			}}},
			{chunk: model.CompletionChunk{Completion: model.Completion{
				Name:    "draft_from_transcript",
				Payload: []byte(`{"assistant_text":"created a draft"}`),
			}}},
		},
	}
	stream, err := Stream(
		t.Context(),
		mustCompletionClient(t, &recordingCompletionClient{streamer: upstream}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, firstErr := stream.Recv()
	_, secondErr := stream.Recv()

	require.Error(t, firstErr)
	require.Equal(t, firstErr, secondErr)
	require.Equal(t, 1, upstream.index)
	_, ok := stream.Value()
	require.False(t, ok)
}

func TestStreamReconcilesCompleteProviderOutput(t *testing.T) {
	tests := []struct {
		name           string
		streamThinking model.ThinkingPart
		responseThink  model.ThinkingPart
		streamUsage    model.TokenUsage
		responseUsage  model.TokenUsage
		streamStop     string
		responseStop   string
		want           string
	}{
		{
			name:           "thinking",
			streamThinking: model.ThinkingPart{Text: "streamed", Final: true},
			responseThink:  model.ThinkingPart{Text: "complete", Final: true},
			streamStop:     "stop",
			responseStop:   "stop",
			want:           "streamed thinking does not match canonical response",
		},
		{
			name:           "usage",
			streamThinking: model.ThinkingPart{Text: "same", Final: true},
			responseThink:  model.ThinkingPart{Text: "same", Final: true},
			streamUsage:    model.TokenUsage{TotalTokens: 1},
			responseUsage:  model.TokenUsage{TotalTokens: 2},
			streamStop:     "stop",
			responseStop:   "stop",
			want:           "stream usage deltas do not match canonical response usage",
		},
		{
			name:           "stop reason",
			streamThinking: model.ThinkingPart{Text: "same", Final: true},
			responseThink:  model.ThinkingPart{Text: "same", Final: true},
			streamStop:     "length",
			responseStop:   "stop",
			want:           `stream stop reason "length" does not match canonical response "stop"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results := []recvResult{
				{chunk: model.ThinkingChunk{Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{test.streamThinking},
				}}},
				{chunk: model.CompletionChunk{Completion: model.Completion{
					Name:    "draft_from_transcript",
					Payload: []byte(`{"assistant_text":"created a draft"}`),
				}}},
			}
			if test.streamUsage != (model.TokenUsage{}) {
				results = append(results, recvResult{chunk: model.UsageChunk{Usage: test.streamUsage}})
			}
			results = append(
				results,
				recvResult{chunk: model.StopChunk{Reason: test.streamStop}},
				recvResult{err: io.EOF},
			)
			stream, err := Stream(
				context.Background(),
				mustCompletionClient(t, &recordingCompletionClient{streamer: &scriptedStreamer{
					response: &model.Response{
						Content: []model.Message{{
							Role: model.ConversationRoleAssistant,
							Parts: []model.Part{
								test.responseThink,
								model.TextPart{Text: `{"assistant_text":"created a draft"}`},
							},
						}},
						StopReason: test.responseStop,
						Usage:      test.responseUsage,
					},
					results: results,
				}}),
				&model.Request{},
				testCompletionSpec(),
			)
			require.NoError(t, err)

			_, err = stream.Recv()
			require.NoError(t, err)
			_, err = stream.Recv()
			require.ErrorContains(t, completionOutputContractCause(t, err), test.want)
			_, ok := stream.Value()
			require.False(t, ok)
		})
	}
}

func TestStreamRejectsChunkAfterStop(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{streamer: &scriptedStreamer{results: []recvResult{
			{chunk: model.CompletionChunk{Completion: model.Completion{
				Name:    "draft_from_transcript",
				Payload: []byte(`{"assistant_text":"created a draft"}`),
			}}},
			{chunk: model.StopChunk{Reason: "stop"}},
			{chunk: model.UsageChunk{Usage: model.TokenUsage{TotalTokens: 1}}},
		}}}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)
	chunk, err := stream.Recv()
	require.Nil(t, chunk)
	require.ErrorContains(t, completionOutputContractCause(t, err), `emitted "usage" after stop`)
}

func TestStreamRejectsEOFBeforeFinalCompletion(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{
			streamer: &scriptedStreamer{results: []recvResult{{err: io.EOF}}},
		}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.ErrorContains(t, completionOutputContractCause(t, err), "ended without a completion")
}

func TestStreamRejectsFinalCompletionWithoutMatchingCanonicalResponse(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{
			streamer: &scriptedStreamer{results: []recvResult{
				{chunk: model.CompletionChunk{Completion: model.Completion{
					Name:    "draft_from_transcript",
					Payload: []byte(`{"assistant_text":"created a draft"}`),
				}}},
				{chunk: model.StopChunk{Reason: "stop"}},
				{err: io.EOF},
			}},
		}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)
	chunk, err := stream.Recv()
	require.Nil(t, chunk)
	require.ErrorContains(t, completionOutputContractCause(t, err), "invalid canonical response")
}

func TestStreamRejectsStopBeforeFinalCompletion(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{
			streamer: &scriptedStreamer{
				results: []recvResult{{chunk: model.StopChunk{Reason: "stop"}}},
			},
		}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.ErrorContains(t, completionOutputContractCause(t, err), "stopped before a completion")
}

func TestStreamRejectsUnexpectedTextChunk(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		mustCompletionClient(t, &recordingCompletionClient{
			streamer: &scriptedStreamer{
				results: []recvResult{{
					chunk: model.TextChunk{
						Message: model.Message{
							Role:  model.ConversationRoleAssistant,
							Parts: []model.Part{model.TextPart{Text: `{"assistant_text":"created a draft"}`}},
						},
					},
				}},
			},
		}),
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.ErrorContains(t, completionOutputContractCause(t, err), "structured output stream emitted text")
}

func TestStreamRejectsInvalidStructuredOutputChunks(t *testing.T) {
	cases := []struct {
		name    string
		results []recvResult
		want    string
	}{
		{
			name: "missing completion delta fields",
			results: []recvResult{{
				chunk: model.CompletionDeltaChunk{},
			}},
			want: "completion delta is missing its name",
		},
		{
			name: "mismatched completion delta name",
			results: []recvResult{{
				chunk: model.CompletionDeltaChunk{
					Delta: model.CompletionDelta{
						Name:  "other",
						Delta: `{"assistant_text":"draft`,
					},
				},
			}},
			want: `completion delta "other" does not match requested completion`,
		},
		{
			name: "missing completion fields",
			results: []recvResult{{
				chunk: model.CompletionChunk{},
			}},
			want: "completion is missing its name",
		},
		{
			name: "mismatched completion name",
			results: []recvResult{{
				chunk: model.CompletionChunk{
					Completion: model.Completion{
						Name:    "other",
						Payload: []byte(`{"assistant_text":"created a draft"}`),
					},
				},
			}},
			want: `stream completion "other" does not match requested completion`,
		},
		{
			name: "duplicate canonical completion",
			results: []recvResult{
				{
					chunk: model.CompletionChunk{
						Completion: model.Completion{
							Name:    "draft_from_transcript",
							Payload: []byte(`{"assistant_text":"created a draft"}`),
						},
					},
				},
				{
					chunk: model.CompletionChunk{
						Completion: model.Completion{
							Name:    "draft_from_transcript",
							Payload: []byte(`{"assistant_text":"created a second draft"}`),
						},
					},
				},
			},
			want: "multiple final completions",
		},
		{
			name: "completion delta after final completion",
			results: []recvResult{
				{
					chunk: model.CompletionChunk{
						Completion: model.Completion{
							Name:    "draft_from_transcript",
							Payload: []byte(`{"assistant_text":"created a draft"}`),
						},
					},
				},
				{
					chunk: model.CompletionDeltaChunk{
						Delta: model.CompletionDelta{
							Name:  "draft_from_transcript",
							Delta: `{"assistant_text":"draft`,
						},
					},
				},
			},
			want: "completion delta after final completion",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := Stream(
				context.Background(),
				mustCompletionClient(t, &recordingCompletionClient{
					streamer: &scriptedStreamer{results: tc.results},
				}),
				&model.Request{},
				testCompletionSpec(),
			)
			require.NoError(t, err)
			_, err = stream.Recv()
			require.Error(t, err)
			require.ErrorContains(t, completionOutputContractCause(t, err), tc.want)
		})
	}
}

func TestCompleteRejectsToolCalls(t *testing.T) {
	_, err := Complete(t.Context(), mustCompletionClient(t, &recordingCompletionClient{response: &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "tool-1",
				Name:  "lookup",
				Input: rawjson.Message(`{}`),
			}},
		}},
		StopReason: "tool_use",
	}}), &model.Request{}, testCompletionSpec())
	require.Error(t, err)
	assert.ErrorContains(t, completionOutputContractCause(t, err), "returned tool calls")
}

func TestCompleteRejectsMultipleAssistantMessages(t *testing.T) {
	_, err := Complete(t.Context(), mustCompletionClient(t, &recordingCompletionClient{response: &model.Response{
		Content: []model.Message{
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: `{"assistant_text":"first"}`}},
			},
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: `{"assistant_text":"second"}`}},
			},
		},
		StopReason: "stop",
	}}), &model.Request{}, testCompletionSpec())
	require.Error(t, err)
	assert.ErrorContains(t, completionOutputContractCause(t, err), "requires exactly one assistant message")
}

func TestCompleteConcatenatesChunkedTextParts(t *testing.T) {
	response, err := Complete(t.Context(), mustCompletionClient(t, &recordingCompletionClient{response: &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.TextPart{Text: `{"assistant_`},
				model.TextPart{Text: `text":"joined"}`},
			},
		}},
		StopReason: "stop",
	}}), &model.Request{}, testCompletionSpec())
	require.NoError(t, err)
	assert.Equal(t, testCompletionResult{AssistantText: "joined"}, response.Value)
}

func TestCompleteRejectsTwoJSONValues(t *testing.T) {
	_, err := Complete(t.Context(), mustCompletionClient(t, &recordingCompletionClient{response: &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.TextPart{Text: `{"assistant_text":"first"}`},
				model.TextPart{Text: `{"assistant_text":"second"}`},
			},
		}},
		StopReason: "stop",
	}}), &model.Request{}, testCompletionSpec())
	require.Error(t, err)
}
