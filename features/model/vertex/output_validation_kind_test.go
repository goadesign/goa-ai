// Vertex output-classification tests drive malformed SDK values through the
// public unary and asynchronous streaming adapters. They verify the exact
// mechanical category returned to callers and keep provider failures outside
// model-output validation.
package vertex

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestVertexCompleteReturnsExactOutputValidationKinds(t *testing.T) {
	tests := []struct {
		name     string
		response *genai.GenerateContentResponse
		request  func(*testing.T) *model.Request
		kind     model.OutputValidationKind
	}{
		{
			name:     "response shape",
			response: &genai.GenerateContentResponse{},
			kind:     model.OutputValidationResponseShape,
		},
		{
			name: "tool identity",
			response: vertexFunctionCallResponse(&genai.FunctionCall{
				ID:   "call-1",
				Name: "unadvertised",
				Args: map[string]any{},
			}),
			request: vertexToolRequest,
			kind:    model.OutputValidationToolIdentity,
		},
		{
			name: "tool arguments",
			response: vertexFunctionCallResponse(&genai.FunctionCall{
				ID:   "call-1",
				Name: "lookup",
				Args: map[string]any{"value": struct{}{}},
			}),
			request: vertexToolRequest,
			kind:    model.OutputValidationToolArguments,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := vertexTextRequest()
			if test.request != nil {
				request = test.request(t)
			}
			client, err := New(
				&stubGenerativeClient{resp: test.response},
				Options{DefaultModel: "gemini-test"},
			)
			require.NoError(t, err)

			_, err = client.Complete(t.Context(), request)

			requireVertexOutputValidationKind(t, err, test.kind)
		})
	}
}

func TestVertexStreamReturnsExactOutputValidationKinds(t *testing.T) {
	tests := []struct {
		name      string
		responses []*genai.GenerateContentResponse
		request   func(*testing.T) *model.Request
		kind      model.OutputValidationKind
	}{
		{
			name: "response shape",
			responses: []*genai.GenerateContentResponse{{
				Candidates: []*genai.Candidate{{}, {}},
			}},
			kind: model.OutputValidationResponseShape,
		},
		{
			name: "stream protocol",
			kind: model.OutputValidationStreamProtocol,
		},
		{
			name: "structured output",
			responses: []*genai.GenerateContentResponse{{
				Candidates: []*genai.Candidate{{
					Content:      &genai.Content{Parts: []*genai.Part{{Text: "{"}}},
					FinishReason: genai.FinishReasonStop,
				}},
			}},
			request: vertexStructuredOutputRequest,
			kind:    model.OutputValidationStructuredOutput,
		},
		{
			name: "tool identity",
			responses: []*genai.GenerateContentResponse{{
				Candidates: []*genai.Candidate{{
					Content: &genai.Content{Parts: []*genai.Part{{
						FunctionCall: &genai.FunctionCall{ID: "call-1", Args: map[string]any{}},
					}}},
					FinishReason: genai.FinishReasonStop,
				}},
			}},
			request: vertexToolRequest,
			kind:    model.OutputValidationToolIdentity,
		},
		{
			name: "tool arguments",
			responses: []*genai.GenerateContentResponse{{
				Candidates: []*genai.Candidate{{
					Content: &genai.Content{Parts: []*genai.Part{{
						FunctionCall: &genai.FunctionCall{
							ID:   "call-1",
							Name: "lookup",
							Args: map[string]any{"value": struct{}{}},
						},
					}}},
					FinishReason: genai.FinishReasonStop,
				}},
			}},
			request: vertexToolRequest,
			kind:    model.OutputValidationToolArguments,
		},
		{
			name: "output bounds",
			responses: []*genai.GenerateContentResponse{{
				Candidates: []*genai.Candidate{{
					Content: &genai.Content{Parts: []*genai.Part{{
						Text: strings.Repeat("x", (16<<20)+1),
					}}},
					FinishReason: genai.FinishReasonStop,
				}},
			}},
			kind: model.OutputValidationOutputBounds,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := vertexTextRequest()
			if test.request != nil {
				request = test.request(t)
			}
			client, err := New(
				&stubGenerativeClient{streamChunks: test.responses},
				Options{DefaultModel: "gemini-test"},
			)
			require.NoError(t, err)
			stream, err := client.Stream(t.Context(), request)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, stream.Close())
			}()

			err = drainToError(t, stream)

			requireVertexOutputValidationKind(t, err, test.kind)
		})
	}
}

func TestVertexProviderFailuresRemainOutsideOutputValidation(t *testing.T) {
	cause := errors.New("provider unavailable")
	t.Run("unary", func(t *testing.T) {
		client, err := New(
			&stubGenerativeClient{err: cause},
			Options{DefaultModel: "gemini-test"},
		)
		require.NoError(t, err)

		_, err = client.Complete(t.Context(), vertexTextRequest())

		requireVertexProviderError(t, err)
	})
	t.Run("stream", func(t *testing.T) {
		client, err := New(
			&stubGenerativeClient{streamErr: cause},
			Options{DefaultModel: "gemini-test"},
		)
		require.NoError(t, err)
		stream, err := client.Stream(t.Context(), vertexTextRequest())
		require.NoError(t, err)
		defer func() {
			require.NoError(t, stream.Close())
		}()

		err = drainToError(t, stream)

		requireVertexProviderError(t, err)
	})
}

// vertexTextRequest returns the smallest valid request shared by adapter tests.
func vertexTextRequest() *model.Request {
	return &model.Request{Messages: []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "Summarize the record."}},
	}}}
}

// vertexToolRequest advertises the one function used by tool-output tests.
func vertexToolRequest(t *testing.T) *model.Request {
	t.Helper()
	request := vertexTextRequest()
	request.Tools = []*model.ToolDefinition{toolDef(t, "lookup", `{"type":"object"}`)}
	return request
}

// vertexStructuredOutputRequest installs the decoder required by the typed
// completion contract before malformed provider JSON reaches the stream.
func vertexStructuredOutputRequest(t *testing.T) *model.Request {
	t.Helper()
	request := vertexTextRequest()
	request.StructuredOutput = &model.StructuredOutput{
		Name:   "result",
		Schema: []byte(`{"type":"object"}`),
	}
	require.NoError(t, model.SetCompletionValidator(
		request,
		func(*model.Response, *model.Completion) error { return nil },
	))
	return request
}

// vertexFunctionCallResponse wraps one SDK function call in an otherwise valid
// unary response so the call itself is the first rejected provider value.
func vertexFunctionCallResponse(call *genai.FunctionCall) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		Content: &genai.Content{Parts: []*genai.Part{{
			FunctionCall: call,
		}}},
		FinishReason: genai.FinishReasonStop,
	}}}
}

// requireVertexOutputValidationKind verifies the public error and its exact
// privacy-safe category without inspecting private provider text.
func requireVertexOutputValidationKind(t *testing.T, err error, kind model.OutputValidationKind) {
	t.Helper()
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, kind, validationErr.Kind())
	require.EqualError(t, validationErr, "model output does not meet its request contract")
	if kind == model.OutputValidationToolArguments {
		require.Empty(t, validationErr.RecoveryCorrection())
	}
}

// requireVertexProviderError verifies transport failures never become model
// output-validation errors.
func requireVertexProviderError(t *testing.T, err error) {
	t.Helper()
	var validationErr *model.OutputValidationError
	require.NotErrorAs(t, err, &validationErr)
	_, providerFailure := model.AsProviderError(err)
	require.True(t, providerFailure)
}
