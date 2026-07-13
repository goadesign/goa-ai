package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestValidateResponse(t *testing.T) {
	tests := []struct {
		name     string
		response *Response
		wantErr  string
	}{
		{
			name: "canonical response",
			response: &Response{
				Content: []Message{{
					Role: ConversationRoleAssistant,
					Parts: []Part{
						ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
						TextPart{Text: "answer"},
						ToolUsePart{ID: "call-1", Name: "svc.lookup", Input: rawjson.Message(`{}`)},
					},
				}},
				StopReason: "tool_use",
			},
		},
		{name: "nil response", wantErr: "response is nil"},
		{
			name:     "missing assistant content",
			response: &Response{StopReason: "end_turn"},
			wantErr:  "response has no assistant content",
		},
		{
			name: "missing stop reason",
			response: &Response{Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "answer"}},
			}}},
			wantErr: "response is missing its stop reason",
		},
		{
			name: "empty assistant message",
			response: &Response{
				Content:    []Message{{Role: ConversationRoleAssistant}},
				StopReason: "end_turn",
			},
			wantErr: "assistant response message has no parts",
		},
		{
			name: "empty assistant text",
			response: &Response{
				Content:    []Message{{Role: ConversationRoleAssistant, Parts: []Part{TextPart{}}}},
				StopReason: "end_turn",
			},
			wantErr: "text is empty",
		},
		{
			name: "empty citations",
			response: &Response{
				Content:    []Message{{Role: ConversationRoleAssistant, Parts: []Part{CitationsPart{Text: "answer"}}}},
				StopReason: "end_turn",
			},
			wantErr: "citation list is empty",
		},
		{
			name: "non-JSON metadata",
			response: &Response{
				Content: []Message{{
					Role:  ConversationRoleAssistant,
					Parts: []Part{TextPart{Text: "answer"}},
					Meta:  map[string]any{"invalid": make(chan int)},
				}},
				StopReason: "end_turn",
			},
			wantErr: "not JSON-compatible metadata",
		},
		{
			name: "empty raw metadata",
			response: &Response{
				Content: []Message{{
					Role:  ConversationRoleAssistant,
					Parts: []Part{TextPart{Text: "answer"}},
					Meta:  map[string]any{"invalid": rawjson.Message{}},
				}},
				StopReason: "end_turn",
			},
			wantErr: "non-nil message is empty",
		},
		{
			name: "non-assistant message",
			response: &Response{Content: []Message{{
				Role:  ConversationRoleUser,
				Parts: []Part{TextPart{Text: "answer"}},
			}}},
			wantErr: "message role must be assistant",
		},
		{
			name: "draft thinking",
			response: &Response{Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{ThinkingPart{Text: "draft"}},
			}}},
			wantErr: "completed response contains draft thinking",
		},
		{
			name: "duplicate tool ID",
			response: &Response{Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{
					ToolUsePart{ID: "call-1", Name: "svc.first", Input: rawjson.Message(`{}`)},
					ToolUsePart{ID: "call-1", Name: "svc.second", Input: rawjson.Message(`{}`)},
				},
			}}},
			wantErr: `duplicate tool call ID "call-1"`,
		},
		{
			name: "invalid tool payload",
			response: &Response{Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID: "call-1", Name: "svc.lookup", Input: rawjson.Message(`{`),
				}},
			}}},
			wantErr: "payload is not valid JSON",
		},
		{
			name: "non-object tool payload",
			response: &Response{Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID: "call-1", Name: "svc.lookup", Input: rawjson.Message(`[]`),
				}},
			}}},
			wantErr: "payload must be a JSON object",
		},
		{
			name: "negative usage",
			response: &Response{
				Content:    []Message{{Role: ConversationRoleAssistant, Parts: []Part{TextPart{Text: "answer"}}}},
				StopReason: "end_turn",
				Usage:      TokenUsage{InputTokens: -1},
			},
			wantErr: "token usage cannot be negative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateResponse(test.response)
			if test.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestValidateChunk(t *testing.T) {
	tests := []struct {
		name    string
		chunk   Chunk
		wantErr string
	}{
		{
			name:    "empty tool call",
			chunk:   ToolCallChunk{},
			wantErr: "missing its ID",
		},
		{
			name:    "empty stop reason",
			chunk:   StopChunk{},
			wantErr: "stop chunk is missing its reason",
		},
		{
			name: "non-assistant text",
			chunk: TextChunk{
				Message: Message{
					Role:  ConversationRoleUser,
					Parts: []Part{TextPart{Text: "answer"}},
				},
			},
			wantErr: "stream message role must be assistant",
		},
		{
			name: "tool use in text chunk",
			chunk: TextChunk{
				Message: Message{
					Role: ConversationRoleAssistant,
					Parts: []Part{ToolUsePart{
						ID:    "call-1",
						Name:  "svc.lookup",
						Input: rawjson.Message(`{}`),
					}},
				},
			},
			wantErr: "text chunk part 0",
		},
		{
			name: "empty text chunk",
			chunk: TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{}},
			}},
			wantErr: "text chunk part 0 is empty",
		},
		{
			name:    "negative usage",
			chunk:   UsageChunk{Usage: TokenUsage{OutputTokens: -1}},
			wantErr: "token usage cannot be negative",
		},
		{
			name:    "nil chunk",
			wantErr: "stream chunk is nil",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateChunk(test.chunk)
			if test.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestCloneResponseIsolatesMutableProviderState(t *testing.T) {
	response := &Response{
		Content: []Message{
			{
				Role: ConversationRoleAssistant,
				Parts: []Part{
					ThinkingPart{Redacted: []byte("opaque"), Final: true},
					CitationsPart{Text: "answer", Citations: []Citation{{
						SourceContent: []string{"source"},
					}}},
					ToolUsePart{ID: "call-1", Name: "svc.lookup", Input: rawjson.Message(`{"q":"x"}`)},
				},
				Meta: map[string]any{
					"items": []string{"first"},
					"nested": map[string]any{
						"value": []byte("bytes"),
					},
				},
			},
			{
				Role: ConversationRoleAssistant,
				Meta: map[string]any{},
			},
		},
	}

	cloned, err := CloneResponse(response)
	require.NoError(t, err)

	response.Content[0].Parts[0].(ThinkingPart).Redacted[0] = 'X'
	response.Content[0].Parts[1].(CitationsPart).Citations[0].SourceContent[0] = "changed-source"
	response.Content[0].Meta["items"].([]string)[0] = "changed-item"
	response.Content[0].Meta["nested"].(map[string]any)["value"].([]byte)[0] = 'X'
	response.Content[1].Meta["late"] = "changed-meta"
	response.Content[0].Parts[2].(ToolUsePart).Input[0] = '['

	assert.Equal(t, []byte("opaque"), cloned.Content[0].Parts[0].(ThinkingPart).Redacted)
	assert.Equal(t, "source", cloned.Content[0].Parts[1].(CitationsPart).Citations[0].SourceContent[0])
	assert.Equal(t, "first", cloned.Content[0].Meta["items"].([]string)[0])
	assert.Equal(t, []byte("bytes"), cloned.Content[0].Meta["nested"].(map[string]any)["value"])
	assert.NotContains(t, cloned.Content[1].Meta, "late")
	assert.JSONEq(t, `{"q":"x"}`, string(cloned.Content[0].Parts[2].(ToolUsePart).Input))
}
