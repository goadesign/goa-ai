package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/openai/openai-go/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestNewRequiresDefaultModel(t *testing.T) {
	_, err := New(Options{transport: &mockTransport{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default model")
}

func TestNewRejectsUnknownThinkingEffort(t *testing.T) {
	_, err := New(Options{
		DefaultModel:   "gpt-4o",
		ThinkingEffort: "extreme",
		transport:      &mockTransport{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported thinking effort")
}

func TestClientCompleteUsesExplicitToolLoopTranscript(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustCompletedResponse(t),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: "Need a tool."},
					model.ToolUsePart{
						ID:    "call_1",
						Name:  "analytics.analyze",
						Input: rawjson.Message(`{"query":"sales"}`),
					},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{model.ToolResultPart{
					ToolUseID: "call_1",
					Content:   map[string]any{"status": "ok"},
				}},
			},
		},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	items := transport.completeRequests[0].Input.OfInputItemList
	require.Len(t, items, 3)
	require.NotNil(t, items[0].OfOutputMessage)
	require.Len(t, items[0].OfOutputMessage.Content, 1)
	assert.Equal(t, "Need a tool.", items[0].OfOutputMessage.Content[0].OfOutputText.Text)
	require.NotNil(t, items[1].OfFunctionCall)
	assert.Equal(t, "call_1", items[1].OfFunctionCall.CallID)
	assert.Equal(t, SanitizeToolName("analytics.analyze"), items[1].OfFunctionCall.Name)
	assert.JSONEq(t, `{"query":"sales"}`, items[1].OfFunctionCall.Arguments)
	require.NotNil(t, items[2].OfFunctionCallOutput)
	assert.Equal(t, "call_1", items[2].OfFunctionCallOutput.CallID)
	assert.JSONEq(t, `{"status":"ok"}`, items[2].OfFunctionCallOutput.Output)
}

func TestClientCompleteRejectsUnrepresentableExplicitTranscript(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustCompletedResponse(t),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "call_1",
					Name:  "analytics.analyze",
					Input: rawjson.Message(`{"query":"sales"}`),
				},
				model.TextPart{Text: "post tool text"},
			},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assistant text after tool_use")
	assert.Empty(t, transport.completeRequests)
}

func TestClientCompleteRejectsThinkingWithoutProviderMetadata(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustCompletedResponse(t),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ThinkingPart{Text: "reasoning", Final: true},
				model.TextPart{Text: "answer"},
			},
		}},
	})

	require.EqualError(t, err, "openai: thinking replay requires provider reasoning metadata")
	assert.Empty(t, transport.completeRequests)
}

func TestClientCompleteRejectsMalformedProviderMetadata(t *testing.T) {
	transport := &mockTransport{completeResponse: mustCompletedResponse(t)}
	client, err := New(Options{DefaultModel: "gpt-4o", transport: transport})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ThinkingPart{Text: "reasoning", Final: true},
				model.TextPart{Text: "answer"},
			},
			Meta: map[string]any{openAIReasoningItemsMetaKey: []any{42}},
		}},
	})

	require.ErrorContains(t, err, `metadata "openai_reasoning_items" item 0 must be a string`)
	assert.Empty(t, transport.completeRequests)
}

func TestClientCompleteLowersRunlogReplayedTranscript(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustCompletedResponse(t),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	messages := replayedToolLoopMessages(t)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: messages,
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	items := transport.completeRequests[0].Input.OfInputItemList
	require.Len(t, items, 5)
	require.NotNil(t, items[0].OfMessage)
	require.NotNil(t, items[1].OfReasoning)
	require.NotNil(t, items[2].OfOutputMessage)
	require.NotNil(t, items[3].OfFunctionCall)
	require.NotNil(t, items[4].OfFunctionCallOutput)
	assert.Equal(t, "call_1", items[3].OfFunctionCall.CallID)
	assert.Equal(t, "call_1", items[4].OfFunctionCallOutput.CallID)
}

func TestClientCompleteEncodesToolLoopTranscript(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{
			"model":"gpt-4o",
			"status":"completed",
			"usage":{
				"input_tokens":11,
				"input_tokens_details":{"cached_tokens":0},
				"output_tokens":7,
				"output_tokens_details":{"reasoning_tokens":0},
				"total_tokens":18
			},
			"output":[
				{
					"id":"msg_1",
					"type":"message",
					"role":"assistant",
					"status":"completed",
					"content":[{"type":"output_text","text":"Need a tool.","annotations":[],"logprobs":[]}]
				},
				{
					"id":"fc_1",
					"type":"function_call",
					"call_id":"call_2",
					"name":"analytics_analyze",
					"arguments":"{\"query\":\"docs\"}",
					"status":"completed"
				}
			]
		}`),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	resp, err := client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "Ping"}},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: "Need a tool."},
					model.ToolUsePart{
						ID:    "call_1",
						Name:  "analytics.analyze",
						Input: rawjson.Message(`{"query":"sales"}`),
					},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{model.ToolResultPart{
					ToolUseID: "call_1",
					Content:   map[string]any{"status": "ok"},
				}},
			},
		},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	request := transport.completeRequests[0]
	items := request.Input.OfInputItemList
	require.Len(t, items, 4)
	assert.Equal(t, "gpt-4o", request.Model)
	assert.False(t, request.Store.Value)
	require.NotNil(t, items[0].OfMessage)
	require.Len(t, items[0].OfMessage.Content.OfInputItemContentList, 1)
	assert.Equal(t, "Ping", items[0].OfMessage.Content.OfInputItemContentList[0].OfInputText.Text)
	require.NotNil(t, items[1].OfOutputMessage)
	require.Len(t, items[1].OfOutputMessage.Content, 1)
	assert.Equal(t, "Need a tool.", items[1].OfOutputMessage.Content[0].OfOutputText.Text)
	require.NotNil(t, items[2].OfFunctionCall)
	assert.Equal(t, SanitizeToolName("analytics.analyze"), items[2].OfFunctionCall.Name)
	assert.Equal(t, "call_1", items[2].OfFunctionCall.CallID)
	assert.JSONEq(t, `{"query":"sales"}`, items[2].OfFunctionCall.Arguments)
	require.NotNil(t, items[3].OfFunctionCallOutput)
	assert.Equal(t, "call_1", items[3].OfFunctionCallOutput.CallID)
	assert.JSONEq(t, `{"status":"ok"}`, items[3].OfFunctionCallOutput.Output)

	require.Len(t, resp.Content, 2)
	assert.Equal(t, model.ConversationRoleAssistant, resp.Content[0].Role)
	text, ok := resp.Content[0].Parts[0].(model.TextPart)
	require.True(t, ok)
	assert.Equal(t, "Need a tool.", text.Text)
	_, ok = resp.Content[1].Parts[0].(model.ToolUsePart)
	require.True(t, ok)

	require.Len(t, resp.ToolCalls(), 1)
	assert.Equal(t, tools.Ident("analytics.analyze"), resp.ToolCalls()[0].Name)
	assert.Equal(t, "call_2", resp.ToolCalls()[0].ID)
	assert.JSONEq(t, `{"query":"docs"}`, string(resp.ToolCalls()[0].Payload))
	assert.Equal(t, "tool_calls", resp.StopReason)
	assert.Equal(t, 18, resp.Usage.TotalTokens)
	assert.Equal(t, "gpt-4o", resp.Usage.Model)
	assert.Equal(t, model.ModelClassDefault, resp.Usage.ModelClass)
}

func TestClientCompleteReplaysHistoricalToolUseUnchanged(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustCompletedResponse(t),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "call_1",
				Name:  "atlas.read.unknown",
				Input: rawjson.Message(`{"from":"2026-04-03T00:00:00Z"}`),
			}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        tools.ToolUnavailable.String(),
			Description: "Report that a previously used tool is unavailable.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	items := transport.completeRequests[0].Input.OfInputItemList
	require.Len(t, items, 1)
	require.NotNil(t, items[0].OfFunctionCall)
	assert.Equal(t, "atlas.read.unknown", items[0].OfFunctionCall.Name)
	assert.JSONEq(t, `{"from":"2026-04-03T00:00:00Z"}`, items[0].OfFunctionCall.Arguments)
}

func TestClientCompleteEncodesToolResultErrorsExplicitly(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustCompletedResponse(t),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{model.ToolUsePart{
					ID:    "call_1",
					Name:  "analytics.analyze",
					Input: rawjson.Message(`{"query":"sales"}`),
				}},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{model.ToolResultPart{
					ToolUseID: "call_1",
					Content:   "analysis backend unavailable",
					IsError:   true,
				}},
			},
		},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	items := transport.completeRequests[0].Input.OfInputItemList
	require.Len(t, items, 2)
	require.NotNil(t, items[1].OfFunctionCallOutput)
	assert.Equal(t, "call_1", items[1].OfFunctionCallOutput.CallID)
	assert.JSONEq(t, `{"is_error":true,"error":"analysis backend unavailable"}`, items[1].OfFunctionCallOutput.Output)
}

func TestClientCompleteRejectsAssistantTextAfterToolUse(t *testing.T) {
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{},
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "call_1",
					Name:  "analytics.analyze",
					Input: rawjson.Message(`{"query":"sales"}`),
				},
				model.TextPart{Text: "post tool text"},
			},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assistant text after tool_use")
}

func TestClientCompleteRoutesModelsAndToolChoice(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustCompletedResponse(t),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		SmallModel:   "gpt-5-mini",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		ModelClass: model.ModelClassSmall,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
		ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeAny},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	request := transport.completeRequests[0]
	assert.Equal(t, "gpt-5-mini", request.Model)
	assert.Equal(t, responses.ToolChoiceOptionsRequired, request.ToolChoice.OfToolChoiceMode.Value)
}

func TestClientCompleteProjectsStrictToolSchemasAndCanonicalizesArguments(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{
			"model":"gpt-4o",
			"status":"completed",
			"output":[
				{
					"id":"fc_1",
					"type":"function_call",
					"call_id":"call_1",
					"name":"helpers_answer",
					"arguments":"{\"question\":\"What is the capital of Japan?\",\"style\":null}",
					"status":"completed"
				}
			]
		}`),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	resp, err := client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "helpers.answer",
			Description: "Answer a simple question.",
			Input: model.ToolInputFromSchema(rawjson.Message(`{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"question": {"type": "string", "description": "User question", "example": "What?"},
					"style": {"type": "string"}
				},
				"example": {"question": "What is the capital of Japan?"},
				"required": ["question"]
			}`)),
		}},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	request := transport.completeRequests[0]
	require.Len(t, request.Tools, 1)
	function := request.Tools[0].OfFunction
	require.NotNil(t, function)
	assert.True(t, function.Strict.Value)
	parameters, err := json.Marshal(function.Parameters)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"question": {"type": "string", "description": "User question"},
			"style": {"type": ["string", "null"]}
		},
		"required": ["question", "style"]
	}`, string(parameters))

	require.Len(t, resp.ToolCalls(), 1)
	assert.Equal(t, tools.Ident("helpers.answer"), resp.ToolCalls()[0].Name)
	assert.JSONEq(t, `{"question":"What is the capital of Japan?"}`, string(resp.ToolCalls()[0].Payload))
}

func TestClientCompleteRejectsMissingRequestedModelClassConfig(t *testing.T) {
	tests := []struct {
		name       string
		modelClass model.ModelClass
		wantErr    string
	}{
		{
			name:       "high reasoning missing",
			modelClass: model.ModelClassHighReasoning,
			wantErr:    "HighModel is not configured",
		},
		{
			name:       "small missing",
			modelClass: model.ModelClassSmall,
			wantErr:    "SmallModel is not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &mockTransport{}
			client, err := New(Options{
				DefaultModel: "gpt-4o",
				transport:    transport,
			})
			require.NoError(t, err)

			_, err = client.Complete(context.Background(), &model.Request{
				ModelClass: tt.modelClass,
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "Ping"}},
				}},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, transport.completeRequests)
		})
	}
}

func TestClientCompleteSupportsStructuredOutput(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{
			"status":"completed",
			"output":[
				{
					"id":"msg_1",
					"type":"message",
					"role":"assistant",
					"status":"completed",
					"content":[{"type":"output_text","text":"{\"answer\":\"ok\"}","annotations":[],"logprobs":[]}]
				}
			]
		}`),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	resp, err := client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: tools.RawJSON(`{"type":"object","additionalProperties":false}`),
		},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	request := transport.completeRequests[0]
	require.NotNil(t, request.Text.Format.OfJSONSchema)
	assert.Equal(t, "draft_from_transcript", request.Text.Format.OfJSONSchema.Name)
	schema, err := json.Marshal(request.Text.Format.OfJSONSchema.Schema)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object","additionalProperties":false}`, string(schema))
	require.Len(t, resp.Content, 1)
	assert.Equal(t, "stop", resp.StopReason)
}

func TestClientCompleteRejectsUnsupportedThinkingShape(t *testing.T) {
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{},
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			Interleaved:  true,
			BudgetTokens: 1024,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "thinking budgets")
}

func TestClientCompleteRejectsRequestCacheOptions(t *testing.T) {
	transport := &mockTransport{}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
		Cache: &model.CacheOptions{AfterSystem: true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request caching is not supported")
	assert.Empty(t, transport.completeRequests)
}

func TestClientCompleteRejectsCacheCheckpointParts(t *testing.T) {
	transport := &mockTransport{}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.TextPart{Text: "Ping"},
				model.CacheCheckpointPart{},
			},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache checkpoints are not supported")
	assert.Empty(t, transport.completeRequests)
}

func TestClientCompleteRejectsStructuredOutputWithTools(t *testing.T) {
	transport := &mockTransport{}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: tools.RawJSON(`{"type":"object"}`),
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "structured output cannot be combined with tools")
	assert.Empty(t, transport.completeRequests)
}

func TestClientCompleteRejectsInvalidToolDefinitions(t *testing.T) {
	tests := []struct {
		name    string
		tools   []*model.ToolDefinition
		wantErr string
	}{
		{
			name:    "nil tool definition",
			tools:   []*model.ToolDefinition{nil},
			wantErr: "tool[0] is nil",
		},
		{
			name: "missing tool name",
			tools: []*model.ToolDefinition{{
				Description: "Run an analysis.",
				Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
			}},
			wantErr: "tool[0] is missing name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &mockTransport{}
			client, err := New(Options{
				DefaultModel: "gpt-4o",
				transport:    transport,
			})
			require.NoError(t, err)

			_, err = client.Complete(context.Background(), &model.Request{
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "Ping"}},
				}},
				Tools: tt.tools,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, transport.completeRequests)
		})
	}
}

func TestOpenAIStreamerEmitsTextToolCallsUsageAndStop(t *testing.T) {
	stream := &mockStream{
		events: []responses.ResponseStreamEventUnion{
			mustStreamEvent(t, `{
				"type":"response.output_text.delta",
				"sequence_number":1,
				"item_id":"msg_1",
				"output_index":0,
				"content_index":0,
				"delta":"Hel",
				"logprobs":[]
			}`),
			mustStreamEvent(t, `{
				"type":"response.output_text.delta",
				"sequence_number":2,
				"item_id":"msg_1",
				"output_index":0,
				"content_index":0,
				"delta":"lo",
				"logprobs":[]
			}`),
			mustStreamEvent(t, `{
				"type":"response.output_item.added",
				"sequence_number":3,
				"output_index":1,
				"item":{
					"id":"fc_1",
					"type":"function_call",
					"call_id":"call_1",
					"name":"analytics_analyze",
					"arguments":"",
					"status":"in_progress"
				}
			}`),
			mustStreamEvent(t, `{
				"type":"response.function_call_arguments.delta",
				"sequence_number":4,
				"item_id":"fc_1",
				"output_index":1,
				"delta":"{\"query\""
			}`),
			mustStreamEvent(t, `{
				"type":"response.function_call_arguments.delta",
				"sequence_number":5,
				"item_id":"fc_1",
				"output_index":1,
				"delta":":\"docs\"}"
			}`),
			mustStreamEvent(t, `{
				"type":"response.completed",
				"sequence_number":6,
				"response":{
					"model":"gpt-4o",
					"status":"completed",
					"usage":{
						"input_tokens":10,
						"input_tokens_details":{"cached_tokens":0},
						"output_tokens":5,
						"output_tokens_details":{"reasoning_tokens":0},
						"total_tokens":15
					},
					"output":[
						{
							"id":"msg_1",
							"type":"message",
							"role":"assistant",
							"status":"completed",
							"content":[{"type":"output_text","text":"Hello","annotations":[],"logprobs":[]}]
						},
						{
							"id":"fc_1",
							"type":"function_call",
							"call_id":"call_1",
							"name":"analytics_analyze",
							"arguments":"{\"query\":\"docs\"}",
							"status":"completed"
						}
					]
				}
			}`),
		},
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{stream: stream},
	})
	require.NoError(t, err)

	streamer, err := client.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.NoError(t, err)
	defer func() {
		_ = streamer.Close()
	}()

	var chunks []model.Chunk
	for {
		chunk, recvErr := streamer.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		require.NoError(t, recvErr)
		chunks = append(chunks, chunk)
	}

	require.Len(t, chunks, 7)
	assert.Equal(t, "Hel", chunks[0].(model.TextChunk).Message.Parts[0].(model.TextPart).Text)
	assert.Equal(t, "lo", chunks[1].(model.TextChunk).Message.Parts[0].(model.TextPart).Text)
	assert.Equal(t, "call_1", chunks[2].(model.ToolCallDeltaChunk).Delta.ID)
	assert.Equal(t, tools.Ident("analytics.analyze"), chunks[2].(model.ToolCallDeltaChunk).Delta.Name)
	require.IsType(t, model.ToolCallDeltaChunk{}, chunks[3])
	call := chunks[4].(model.ToolCallChunk).ToolCall
	assert.Equal(t, "call_1", call.ID)
	assert.JSONEq(t, `{"query":"docs"}`, string(call.Payload))
	assert.Equal(t, 15, chunks[5].(model.UsageChunk).Usage.TotalTokens)
	assert.Equal(t, "tool_calls", chunks[6].(model.StopChunk).Reason)
	require.NotNil(t, streamer.Response())

	meta := streamer.Metadata()
	require.NotNil(t, meta)
	usage, ok := meta["usage"].(model.TokenUsage)
	require.True(t, ok)
	assert.Equal(t, 15, usage.TotalTokens)
	assert.Equal(t, "gpt-4o", usage.Model)
}

func TestOpenAIStreamerHandlesIncompleteResponse(t *testing.T) {
	stream := &mockStream{
		events: []responses.ResponseStreamEventUnion{
			mustStreamEvent(t, `{
				"type":"response.incomplete",
				"sequence_number":1,
				"response":{
					"model":"gpt-4o",
					"status":"incomplete",
					"incomplete_details":{"reason":"max_output_tokens"},
					"usage":{
						"input_tokens":10,
						"input_tokens_details":{"cached_tokens":0},
						"output_tokens":5,
						"output_tokens_details":{"reasoning_tokens":0},
						"total_tokens":15
					},
					"output":[
						{
							"id":"msg_1",
							"type":"message",
							"role":"assistant",
							"status":"completed",
							"content":[{"type":"output_text","text":"Hello","annotations":[],"logprobs":[]}]
						}
					]
				}
			}`),
		},
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{stream: stream},
	})
	require.NoError(t, err)

	streamer, err := client.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
	})
	require.NoError(t, err)
	defer func() {
		_ = streamer.Close()
	}()

	var chunks []model.Chunk
	for {
		chunk, recvErr := streamer.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		require.NoError(t, recvErr)
		chunks = append(chunks, chunk)
	}

	require.Len(t, chunks, 3)
	assert.Equal(t, "Hello", chunks[0].(model.TextChunk).Message.Parts[0].(model.TextPart).Text)
	assert.Equal(t, 15, chunks[1].(model.UsageChunk).Usage.TotalTokens)
	assert.Equal(t, "max_output_tokens", chunks[2].(model.StopChunk).Reason)
	require.NotNil(t, streamer.Response())
}

func TestOpenAIStreamerStructuredOutput(t *testing.T) {
	stream := &mockStream{
		events: []responses.ResponseStreamEventUnion{
			mustStreamEvent(t, `{
				"type":"response.output_text.delta",
				"sequence_number":1,
				"item_id":"msg_1",
				"output_index":0,
				"content_index":0,
				"delta":"{\"answer\":\"ok\"}",
				"logprobs":[]
			}`),
			mustStreamEvent(t, `{
				"type":"response.completed",
				"sequence_number":2,
				"response":{
					"model":"gpt-4o",
					"status":"completed",
					"output":[
						{
							"id":"msg_1",
							"type":"message",
							"role":"assistant",
							"status":"completed",
							"content":[{"type":"output_text","text":"{\"answer\":\"ok\"}","annotations":[],"logprobs":[]}]
						}
					]
				}
			}`),
		},
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{stream: stream},
	})
	require.NoError(t, err)

	streamer, err := client.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: tools.RawJSON(`{"type":"object"}`),
		},
	})
	require.NoError(t, err)
	defer func() {
		_ = streamer.Close()
	}()

	var chunks []model.Chunk
	for {
		chunk, recvErr := streamer.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		require.NoError(t, recvErr)
		chunks = append(chunks, chunk)
	}

	require.Len(t, chunks, 3)
	delta := chunks[0].(model.CompletionDeltaChunk).Delta
	assert.Equal(t, "draft_from_transcript", delta.Name)
	assert.JSONEq(t, `{"answer":"ok"}`, delta.Delta)
	completion := chunks[1].(model.CompletionChunk).Completion
	assert.Equal(t, "draft_from_transcript", completion.Name)
	assert.JSONEq(t, `{"answer":"ok"}`, string(completion.Payload))
	assert.Equal(t, "stop", chunks[2].(model.StopChunk).Reason)
	require.NotNil(t, streamer.Response())
}

type mockTransport struct {
	completeResponse *responses.Response
	completeErr      error
	stream           responseStream

	completeRequests []responses.ResponseNewParams
	streamRequests   []responses.ResponseNewParams
}

func (m *mockTransport) Complete(_ context.Context, request responses.ResponseNewParams) (*responses.Response, error) {
	m.completeRequests = append(m.completeRequests, request)
	return m.completeResponse, m.completeErr
}

func (m *mockTransport) Stream(_ context.Context, request responses.ResponseNewParams) responseStream {
	m.streamRequests = append(m.streamRequests, request)
	return m.stream
}

type mockStream struct {
	events   []responses.ResponseStreamEventUnion
	index    int
	err      error
	closeErr error
}

func (m *mockStream) Next() bool {
	if m.index >= len(m.events) || m.err != nil {
		return false
	}
	m.index++
	return true
}

func (m *mockStream) Current() responses.ResponseStreamEventUnion {
	return m.events[m.index-1]
}

func (m *mockStream) Err() error {
	return m.err
}

func (m *mockStream) Close() error {
	return m.closeErr
}

func mustResponse(t *testing.T, raw string) *responses.Response {
	t.Helper()
	var resp responses.Response
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	return &resp
}

func mustCompletedResponse(t *testing.T) *responses.Response {
	t.Helper()
	return mustResponse(t, `{
		"status":"completed",
		"output":[{
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"status":"completed",
			"content":[{"type":"output_text","text":"ok","annotations":[],"logprobs":[]}]
		}]
	}`)
}

func mustStreamEvent(t *testing.T, raw string) responses.ResponseStreamEventUnion {
	t.Helper()
	var event responses.ResponseStreamEventUnion
	require.NoError(t, json.Unmarshal([]byte(raw), &event))
	return event
}

func replayedToolLoopMessages(t *testing.T) []*model.Message {
	t.Helper()

	ctx := context.Background()
	store := runloginmem.New()
	appendReplayTranscriptDelta(t, ctx, store, []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "Summarize sales"}},
	}})
	appendReplayTranscriptDelta(t, ctx, store, []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ThinkingPart{Text: "Need the sales data first.", Index: 0, Final: true},
			model.TextPart{Text: "Need the sales data first."},
			model.ToolUsePart{
				ID:    "call_1",
				Name:  "analytics.analyze",
				Input: rawjson.Message(`{"query":"sales"}`),
			},
		},
		Meta: map[string]any{
			openAIReasoningItemsMetaKey: []string{
				`{"id":"rs_1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"Need the sales data first."}]}`,
			},
			openAIOutputItemMetaKey: `{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Need the sales data first."}]}`,
		},
	}})
	appendReplayTranscriptDelta(t, ctx, store, []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call_1",
			Content:   map[string]any{"status": "ok", "rows": 3},
		}},
	}})

	messages, err := transcript.BuildMessagesFromRunLog(ctx, store, "run-1")
	require.NoError(t, err)
	return messages
}

func appendReplayTranscriptDelta(t *testing.T, ctx context.Context, store runlog.Store, messages []*model.Message) {
	t.Helper()

	payload, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)

	_, err = store.Append(ctx, &runlog.Event{
		EventKey:  time.Now().UTC().Format(time.RFC3339Nano),
		RunID:     "run-1",
		AgentID:   agent.Ident("agent-1"),
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      transcript.RunLogMessagesAppended,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, err)
}
