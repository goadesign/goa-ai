// These tests run synthetic responses through the private runtime. Both request
// choices must return only an accepted tool value, and rejected responses must
// not execute an earlier call while the runtime requests corrected arguments.
package tooloutput

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/completion"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestToolOutputChoicesPreserveCorrectionAndCompletion(t *testing.T) {
	for _, choice := range []struct {
		name string
		mode model.ToolChoiceMode
		run  func(context.Context, model.Client, *model.Request, completion.Spec[testOutput]) (testOutput, error)
	}{
		{"forced", model.ToolChoiceModeTool, Run[testOutput]},
		{"automatic", model.ToolChoiceModeAuto, RunAutomatic[testOutput]},
	} {
		t.Run(choice.name, func(t *testing.T) {
			for _, input := range []struct {
				name    string
				payload string
			}{
				{"malformed", `{`},
				{"invalid schema", `{"value":7}`},
				{"invalid codec", `{"value":"reject"}`},
				{"invalid multiple calls", `{"value":"must not execute"}`},
				{"wrong tool", `{"value":"must not execute"}`},
			} {
				t.Run(input.name, func(t *testing.T) {
					rejected := toolResponse(input.payload)
					if input.name == "wrong tool" {
						rejected.Content[0].Parts[0] = model.ToolUsePart{
							ID: "call-1", Name: "results.other", Input: rawjson.Message(input.payload),
						}
					}
					if input.name == "invalid multiple calls" {
						rejected.Content[0].Parts = append(rejected.Content[0].Parts, model.ToolUsePart{
							ID: "call-2", Name: "results.submit", Input: rawjson.Message(`{"value":7}`),
						})
					}
					provider := &recordingProvider{responses: []*model.Response{
						rejected, toolResponse(`{"value":"accepted"}`),
					}}
					spec := outputSpec()
					decode := spec.Codec.FromJSON
					spec.Codec.FromJSON = func(data []byte) (testOutput, error) {
						value, err := decode(data)
						if err != nil {
							return testOutput{}, err
						}
						if value.Value == "reject" {
							return testOutput{}, tools.NewValidationError(
								"value is not accepted by the result contract",
								[]*tools.FieldIssue{{Field: "value", Constraint: "invalid_enum_value"}},
								nil,
							)
						}
						return value, nil
					}
					encode := spec.Codec.ToJSON
					var encoded []testOutput
					spec.Codec.ToJSON = func(value testOutput) ([]byte, error) {
						encoded = append(encoded, value)
						return encode(value)
					}
					request := outputRequest()

					value, err := choice.run(t.Context(), testClient(t, provider), request, spec)

					require.NoError(t, err)
					assert.Equal(t, testOutput{Value: "accepted"}, value)
					require.NotEmpty(t, encoded)
					for _, value := range encoded {
						assert.Equal(t, testOutput{Value: "accepted"}, value, "rejected calls must not execute")
					}
					require.Len(t, provider.requests, 2)
					for _, sent := range provider.requests {
						require.NotNil(t, sent.ToolChoice)
						assert.Equal(t, choice.mode, sent.ToolChoice.Mode)
						if choice.mode == model.ToolChoiceModeAuto {
							assert.Empty(t, sent.ToolChoice.Name)
						} else {
							assert.Equal(t, "results.submit", sent.ToolChoice.Name)
						}
						assert.Nil(t, sent.StructuredOutput, "ordinary tool output requires no native JSON support")
						require.Len(t, sent.Tools, 1)
						assert.Equal(t, []byte(spec.Schema), []byte(sent.Tools[0].Input.Contract().Schema))
					}
					assert.Contains(t, systemText(provider.requests[1]), "system-reminder")
					assert.Nil(t, request.ToolChoice)
					assert.Empty(t, request.Tools)
				})
			}
		})
	}
}

func TestRunAutomaticRejectsUnacceptedResponses(t *testing.T) {
	for _, test := range []struct {
		name       string
		stopReason string
		calls      int
		limited    bool
	}{
		{name: "ordinary text", stopReason: "end_turn"},
		{name: "refusal without tool", stopReason: "refusal"},
		{name: "context limit without tool", stopReason: "model_context_window_exceeded"},
		{name: "multiple valid calls", stopReason: "tool_use", calls: 2},
		{name: "valid limited tool", stopReason: "max_tokens", calls: 1, limited: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			parts := make([]model.Part, 0, 1+test.calls)
			parts = append(parts, model.TextPart{Text: `{"value":"text is not a result"}`})
			for index := range test.calls {
				parts = append(parts, model.ToolUsePart{
					ID: fmt.Sprintf("call-%d", index), Name: "results.submit", Input: rawjson.Message(`{"value":"valid"}`),
				})
			}
			provider := &recordingProvider{responses: []*model.Response{{
				Content:       []model.Message{{Role: model.ConversationRoleAssistant, Parts: parts}},
				StopReason:    test.stopReason,
				OutputLimited: test.limited,
			}}}
			spec := outputSpec()
			encode := spec.Codec.ToJSON
			encodes := 0
			spec.Codec.ToJSON = func(value testOutput) ([]byte, error) {
				encodes++
				return encode(value)
			}

			value, err := RunAutomatic(t.Context(), testClient(t, provider), outputRequest(), spec)

			require.Error(t, err)
			assert.Equal(t, testOutput{}, value)
			var failure *RunError
			require.ErrorAs(t, err, &failure)
			var contract *planner.OutputContractError
			require.ErrorAs(t, failure.TerminalError(), &contract)
			assert.Len(t, provider.requests, 1)
			assert.Zero(t, encodes)
		})
	}
}

func TestRunAutomaticPreservesTerminalFailureAfterCorrection(t *testing.T) {
	for _, cause := range []error{errors.New("provider unavailable"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			provider := &recordingProvider{
				responses: []*model.Response{toolResponse(`{"value":7}`), nil},
				errors:    []error{nil, cause},
			}
			value, err := RunAutomatic(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
			assert.Equal(t, testOutput{}, value)
			var failure *RunError
			require.ErrorAs(t, err, &failure)
			require.ErrorIs(t, failure.TerminalError(), cause)
			var rejected *model.OutputValidationError
			assert.NotErrorAs(t, failure.TerminalError(), &rejected)
			require.ErrorAs(t, err, &rejected)
			assert.Len(t, provider.requests, 2)
		})
	}
}

func TestRunAutomaticPreservesCorrectionLimit(t *testing.T) {
	provider := &recordingProvider{responses: []*model.Response{
		toolResponse(`{"value":7}`), toolResponse(`{"value":7}`),
		toolResponse(`{"value":7}`), toolResponse(`{"value":7}`),
	}}
	value, err := RunAutomatic(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
	assert.Equal(t, testOutput{}, value)
	var failure *RunError
	require.ErrorAs(t, err, &failure)
	require.ErrorContains(t, failure.TerminalError(), "recovery_cap")
	assert.Len(t, provider.requests, 4)
}

func TestRunAutomaticPreservesInternalDecoderFailure(t *testing.T) {
	cause := errors.New("decoder implementation failed")
	spec := outputSpec()
	spec.Codec.FromJSON = func([]byte) (testOutput, error) {
		return testOutput{}, cause
	}
	provider := &recordingProvider{responses: []*model.Response{toolResponse(`{"value":"valid"}`)}}

	value, err := RunAutomatic(t.Context(), testClient(t, provider), outputRequest(), spec)

	assert.Equal(t, testOutput{}, value)
	var failure *RunError
	require.ErrorAs(t, err, &failure)
	require.ErrorIs(t, err, cause)
	require.ErrorContains(t, failure.TerminalError(), cause.Error())
	assert.Len(t, provider.requests, 1)
}

func TestRunAutomaticRejectsRequestOverridesBeforeInference(t *testing.T) {
	for _, request := range []*model.Request{
		nil,
		{Tools: []*model.ToolDefinition{{Name: "other"}}},
		{ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "other"}},
		{StructuredOutput: &model.StructuredOutput{}},
		{Stream: true},
	} {
		provider := &recordingProvider{}
		value, err := RunAutomatic(t.Context(), testClient(t, provider), request, outputSpec())
		assert.Equal(t, testOutput{}, value)
		var failure *RunError
		require.ErrorAs(t, err, &failure)
		assert.Empty(t, provider.requests)
	}
}
