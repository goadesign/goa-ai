// These tests verify that streamed assistant content matches the complete
// provider response before callers can accept it.
package model

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRequestContractSupportsConcurrentResponseValidation(t *testing.T) {
	request := &Request{StructuredOutput: &StructuredOutput{
		Name:   "answer",
		Schema: []byte(`{"type":"object"}`),
	}}
	require.NoError(t, SetCompletionValidator(request, func(*Response, *Completion) error {
		return nil
	}))
	response := &Response{
		Content: []Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: `{"answer":"ok"}`}},
		}},
		StopReason: "stop",
	}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)

	const calls = 32
	var wait sync.WaitGroup
	errs := make(chan error, calls)
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := contract.ValidateResponse(response)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, TokenUsage{}, response.Usage)
}

func TestStreamValidatorRejectsMismatchedOutputLimitState(t *testing.T) {
	validator := mustNewStreamValidator(t, &Request{})
	require.NoError(t, validator.accept(TextChunk{Message: Message{
		Role:  ConversationRoleAssistant,
		Parts: []Part{TextPart{Text: "partial"}},
	}}))
	require.NoError(t, validator.accept(StopChunk{
		Reason:        "max_tokens",
		OutputLimited: true,
	}))

	err := validator.finish(&Response{
		Content: []Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "partial"}},
		}},
		StopReason: "max_tokens",
	})

	require.ErrorContains(t, err, "stream output-limit state does not match canonical response")
}

func TestRequestContractAppliesGeneratedToolPayloadCodec(t *testing.T) {
	request := &Request{Tools: []*ToolDefinition{strictToolDefinition()}}
	response := responseWithToolCall(ToolCall{
		ID:      "call-1",
		Name:    "svc.lookup",
		Payload: []byte(`{"query":42}`),
	})

	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	_, err = contract.ValidateResponse(response)

	require.ErrorContains(t, err, `model tool "svc.lookup" payload failed its request contract`)
}

func TestStreamValidatorAppliesGeneratedToolPayloadCodecBeforeExposure(t *testing.T) {
	validator := mustNewStreamValidator(t, &Request{Tools: []*ToolDefinition{strictToolDefinition()}})

	err := validator.accept(ToolCallChunk{ToolCall: ToolCall{
		ID:      "call-1",
		Name:    "svc.lookup",
		Payload: []byte(`{"query":42}`),
	}})

	require.ErrorContains(t, err, `model tool "svc.lookup" payload failed its request contract`)
}

func TestStreamValidatorReconcilesToolPayloadDeltas(t *testing.T) {
	validator := mustNewStreamValidator(t, requestWithTool("svc.lookup"))
	require.NoError(t, validator.accept(ToolCallDeltaChunk{Delta: ToolCallDelta{
		ID:    "call-1",
		Name:  "svc.lookup",
		Delta: `{"query":`,
	}}))
	require.NoError(t, validator.accept(ToolCallDeltaChunk{Delta: ToolCallDelta{
		ID:    "call-1",
		Name:  "svc.lookup",
		Delta: `"ok"}`,
	}}))

	err := validator.accept(ToolCallChunk{ToolCall: ToolCall{
		ID:      "call-1",
		Name:    "svc.lookup",
		Payload: []byte(`{"query":"different"}`),
	}})

	require.ErrorContains(t, err, "payload that differs from its deltas")
}

func TestStreamValidatorRejectsUnfinishedToolPayloadDeltas(t *testing.T) {
	validator := mustNewStreamValidator(t, requestWithTool("svc.lookup"))
	require.NoError(t, validator.accept(ToolCallDeltaChunk{Delta: ToolCallDelta{
		ID:    "call-1",
		Name:  "svc.lookup",
		Delta: `{"query":`,
	}}))

	err := validator.accept(StopChunk{Reason: "tool_use"})

	require.ErrorContains(t, err, `stopped before tool call "call-1" was finalized`)
}

func TestRequestContractAllowsCallerAuthoredToolPayload(t *testing.T) {
	request := requestWithTool("svc.lookup")
	response := responseWithToolCall(ToolCall{
		ID:      "call-1",
		Name:    "svc.lookup",
		Payload: []byte(`{"query":42}`),
	})

	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	_, err = contract.ValidateResponse(response)
	require.NoError(t, err)
}

func TestStreamValidatorReconcilesCitationText(t *testing.T) {
	citations := []Citation{{Title: "source"}}
	validator := mustNewStreamValidator(t, &Request{})
	require.NoError(t, validator.accept(TextChunk{Message: Message{
		Role: ConversationRoleAssistant,
		Parts: []Part{
			TextPart{Text: "plain "},
			CitationsPart{Text: "cited", Citations: citations},
		},
	}}))
	require.NoError(t, validator.accept(StopChunk{Reason: "stop"}))

	err := validator.finish(&Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{
				TextPart{Text: "plain "},
				CitationsPart{Text: "different", Citations: citations},
			},
		}},
		StopReason: "stop",
	})

	require.EqualError(t, err, "streamed text does not match canonical response")
}

func TestStreamValidatorReconcilesCitationDetails(t *testing.T) {
	validator := mustNewStreamValidator(t, &Request{})
	require.NoError(t, validator.accept(TextChunk{Message: Message{
		Role: ConversationRoleAssistant,
		Parts: []Part{CitationsPart{
			Text:      "cited",
			Citations: []Citation{{Title: "streamed source"}},
		}},
	}}))
	require.NoError(t, validator.accept(StopChunk{Reason: "stop"}))

	err := validator.finish(&Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{CitationsPart{
				Text:      "cited",
				Citations: []Citation{{Title: "complete source"}},
			}},
		}},
		StopReason: "stop",
	})

	require.EqualError(t, err, "streamed citations do not match canonical response")
}

func TestStreamValidatorOwnsRetainedMessageParts(t *testing.T) {
	sourceContent := []string{"original excerpt"}
	location := &DocumentPageLocation{Start: 2, End: 2}
	redacted := []byte("original reasoning")
	validator := mustNewStreamValidator(t, &Request{})
	require.NoError(t, validator.accept(TextChunk{Message: Message{
		Role: ConversationRoleAssistant,
		Parts: []Part{CitationsPart{
			Text: "cited",
			Citations: []Citation{{
				Title:         "source",
				SourceContent: sourceContent,
				Location:      CitationLocation{DocumentPage: location},
			}},
		}},
	}}))
	require.NoError(t, validator.accept(ThinkingChunk{Message: Message{
		Role: ConversationRoleAssistant,
		Parts: []Part{ThinkingPart{
			Redacted: redacted,
			Final:    true,
		}},
	}}))

	sourceContent[0] = "changed excerpt"
	location.Start = 9
	redacted[0] = 'X'

	require.NoError(t, validator.accept(StopChunk{Reason: "stop"}))
	require.NoError(t, validator.finish(&Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{
				CitationsPart{
					Text: "cited",
					Citations: []Citation{{
						Title:         "source",
						SourceContent: []string{"original excerpt"},
						Location: CitationLocation{
							DocumentPage: &DocumentPageLocation{Start: 2, End: 2},
						},
					}},
				},
				ThinkingPart{
					Redacted: []byte("original reasoning"),
					Final:    true,
				},
			},
		}},
		StopReason: "stop",
	}))
}

func TestStreamValidatorRejectsTerminalThinkingWithoutFinalChunk(t *testing.T) {
	validator := mustNewStreamValidator(t, &Request{})
	require.NoError(t, validator.accept(StopChunk{Reason: "stop"}))

	err := validator.finish(&Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{ThinkingPart{
				Signature: "signature",
				Final:     true,
			}},
		}},
		StopReason: "stop",
	})

	require.ErrorContains(t, err, "streamed thinking does not match canonical response")
}

func TestStreamValidatorOwnsRetainedToolPayload(t *testing.T) {
	payload := []byte(`{"query":"original"}`)
	validator := mustNewStreamValidator(t, requestWithTool("search"))
	require.NoError(t, validator.accept(ToolCallChunk{ToolCall: ToolCall{
		Name:    "search",
		Payload: payload,
		ID:      "call-1",
	}}))

	payload[10] = 'X'

	require.NoError(t, validator.accept(StopChunk{Reason: "tool_use"}))
	require.NoError(t, validator.finish(&Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{ToolUsePart{
				ID:    "call-1",
				Name:  "search",
				Input: []byte(`{"query":"original"}`),
			}},
		}},
		StopReason: "tool_use",
	}))
}

func TestStreamValidatorOwnsRetainedCompletionPayload(t *testing.T) {
	payload := []byte(`{"result":"original"}`)
	validator := mustNewStreamValidator(t, &Request{
		StructuredOutput: &StructuredOutput{
			Name:   "answer",
			Schema: []byte(`{"type":"object"}`),
		},
	})
	require.NoError(t, validator.accept(CompletionChunk{Completion: Completion{
		Name:    "answer",
		Payload: payload,
	}}))

	payload[11] = 'X'

	require.NoError(t, validator.accept(StopChunk{Reason: "stop"}))
	require.NoError(t, validator.finish(&Response{
		Content: []Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: `{"result":"original"}`}},
		}},
		StopReason: "stop",
	}))
}

func TestStreamValidatorRejectsUsageIdentityChanges(t *testing.T) {
	validator := mustNewStreamValidator(t, &Request{})
	require.NoError(t, validator.accept(UsageChunk{Usage: TokenUsage{
		Model:       "first",
		TotalTokens: 1,
	}}))

	err := validator.accept(UsageChunk{Usage: TokenUsage{
		Model:       "second",
		TotalTokens: 1,
	}})

	require.EqualError(t, err, "model stream changed usage model")
}

func TestStreamValidatorRejectsUsageOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	validator := mustNewStreamValidator(t, &Request{})
	require.NoError(t, validator.accept(UsageChunk{Usage: TokenUsage{
		TotalTokens: maxInt,
	}}))

	err := validator.accept(UsageChunk{Usage: TokenUsage{
		TotalTokens: 1,
	}})

	require.EqualError(t, err, "model stream total token usage exceeds the supported integer range")
}

func TestStreamValidatorReconcilesToolCallSignature(t *testing.T) {
	validator := mustNewStreamValidator(t, requestWithTool("search"))
	require.NoError(t, validator.accept(ToolCallChunk{ToolCall: ToolCall{
		Name:             "search",
		Payload:          []byte(`{"query":"status"}`),
		ID:               "call-1",
		ThoughtSignature: "stream-signature",
	}}))
	require.NoError(t, validator.accept(StopChunk{Reason: "tool_use"}))

	err := validator.finish(&Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{ToolUsePart{
				ID:               "call-1",
				Name:             "search",
				Input:            []byte(`{"query":"status"}`),
				ThoughtSignature: "response-signature",
			}},
		}},
		StopReason: "tool_use",
	})

	require.EqualError(t, err, "stream tool call 0 does not match canonical response")
}

func mustNewStreamValidator(t *testing.T, request *Request) *streamValidator {
	t.Helper()
	if request.StructuredOutput != nil {
		require.NoError(t, SetCompletionValidator(
			request,
			func(*Response, *Completion) error { return nil },
		))
	}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	return newStreamValidator(contract)
}

func requestWithTool(name string) *Request {
	return &Request{Tools: []*ToolDefinition{{
		Name:  name,
		Input: mustAdvertisedToolInput([]byte(`{"type":"object"}`)),
	}}}
}

func strictToolDefinition() *ToolDefinition {
	return ToolDefinitionFromSpec(tools.ToolSpec{
		Name: "svc.lookup",
		Payload: tools.TypeSpec{
			Name:   "LookupPayload",
			Schema: []byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					var payload struct {
						Query string `json:"query"`
					}
					if err := json.Unmarshal(data, &payload); err != nil {
						return nil, err
					}
					return payload, nil
				},
			},
		},
	})
}

func responseWithToolCall(call ToolCall) *Response {
	return &Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{ToolUsePart{
				ID:    call.ID,
				Name:  call.Name.String(),
				Input: call.Payload,
			}},
		}},
		StopReason: "tool_use",
	}
}
