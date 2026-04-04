package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/openai/openai-go/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
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

func TestClientCompleteRunIDPrependsLedgerMessages(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{"status":"completed","output":[]}`),
	}
	ledger := &stubLedgerSource{
		messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.TextPart{Text: "Need a tool."},
				model.ToolUsePart{
					ID:    "call_1",
					Name:  "analytics.analyze",
					Input: map[string]any{"query": "sales"},
				},
			},
		}},
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		Ledger:       ledger,
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		RunID: "run-123",
		Messages: []*model.Message{{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{model.ToolResultPart{
				ToolUseID: "call_1",
				Content:   map[string]any{"status": "ok"},
			}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)

	require.Equal(t, "run-123", ledger.runID)
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

func TestClientCompleteRunIDRejectsUnrepresentableLedgerTranscript(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{"status":"completed","output":[]}`),
	}
	ledger := &stubLedgerSource{
		messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "call_1",
					Name:  "analytics.analyze",
					Input: map[string]any{"query": "sales"},
				},
				model.TextPart{Text: "post tool text"},
			},
		}},
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		Ledger:       ledger,
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		RunID: "run-123",
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assistant text after tool_use")
	assert.Empty(t, transport.completeRequests)
}

func TestClientCompleteRunIDRequiresLedgerSource(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{"status":"completed","output":[]}`),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		RunID: "run-123",
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RunID requires a configured ledger source")
	assert.Empty(t, transport.completeRequests)
}

func TestClientCompleteRunIDPropagatesLedgerErrors(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{"status":"completed","output":[]}`),
	}
	ledger := &stubLedgerSource{err: errors.New("query failed")}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		Ledger:       ledger,
		transport:    transport,
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), &model.Request{
		RunID: "run-123",
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Ping"}},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `load messages for run "run-123"`)
	assert.Contains(t, err.Error(), "query failed")
	assert.Empty(t, transport.completeRequests)
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
						Input: map[string]any{"query": "sales"},
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
			InputSchema: map[string]any{"type": "object"},
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

	require.Len(t, resp.Content, 1)
	assert.Equal(t, model.ConversationRoleAssistant, resp.Content[0].Role)
	text, ok := resp.Content[0].Parts[0].(model.TextPart)
	require.True(t, ok)
	assert.Equal(t, "Need a tool.", text.Text)

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, tools.Ident("analytics.analyze"), resp.ToolCalls[0].Name)
	assert.Equal(t, "call_2", resp.ToolCalls[0].ID)
	assert.JSONEq(t, `{"query":"docs"}`, string(resp.ToolCalls[0].Payload))
	assert.Equal(t, "tool_calls", resp.StopReason)
	assert.Equal(t, 18, resp.Usage.TotalTokens)
	assert.Equal(t, "gpt-4o", resp.Usage.Model)
	assert.Equal(t, model.ModelClassDefault, resp.Usage.ModelClass)
}

func TestClientCompleteRewritesUnknownToolUseToToolUnavailable(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{"status":"completed","output":[]}`),
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
				Input: map[string]any{"from": "2026-04-03T00:00:00Z"},
			}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        tools.ToolUnavailable.String(),
			Description: "Report that a previously used tool is unavailable.",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	items := transport.completeRequests[0].Input.OfInputItemList
	require.Len(t, items, 1)
	require.NotNil(t, items[0].OfFunctionCall)
	assert.Equal(t, SanitizeToolName(tools.ToolUnavailable.String()), items[0].OfFunctionCall.Name)
	assert.JSONEq(t, `{"requested_tool":"atlas.read.unknown","requested_payload":{"from":"2026-04-03T00:00:00Z"}}`, items[0].OfFunctionCall.Arguments)
}

func TestClientCompleteEncodesToolResultErrorsExplicitly(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{"status":"completed","output":[]}`),
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
					Input: map[string]any{"query": "sales"},
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
			InputSchema: map[string]any{"type": "object"},
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
					Input: map[string]any{"query": "sales"},
				},
				model.TextPart{Text: "post tool text"},
			},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assistant text after tool_use")
}

func TestClientCompleteRoutesModelsAndToolChoice(t *testing.T) {
	transport := &mockTransport{
		completeResponse: mustResponse(t, `{"model":"gpt-5-mini","status":"completed","output":[]}`),
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
			InputSchema: map[string]any{"type": "object"},
		}},
		ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeAny},
	})
	require.NoError(t, err)

	require.Len(t, transport.completeRequests, 1)
	request := transport.completeRequests[0]
	assert.Equal(t, "gpt-5-mini", request.Model)
	assert.Equal(t, responses.ToolChoiceOptionsRequired, request.ToolChoice.OfToolChoiceMode.Value)
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
			Schema: []byte(`{"type":"object","additionalProperties":false}`),
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
			InputSchema: map[string]any{"type": "object"},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: []byte(`{"type":"object"}`),
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
				InputSchema: map[string]any{"type": "object"},
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
			InputSchema: map[string]any{"type": "object"},
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
	assert.Equal(t, model.ChunkTypeText, chunks[0].Type)
	assert.Equal(t, "Hel", chunks[0].Message.Parts[0].(model.TextPart).Text)
	assert.Equal(t, model.ChunkTypeText, chunks[1].Type)
	assert.Equal(t, "lo", chunks[1].Message.Parts[0].(model.TextPart).Text)
	assert.Equal(t, model.ChunkTypeToolCallDelta, chunks[2].Type)
	assert.Equal(t, "call_1", chunks[2].ToolCallDelta.ID)
	assert.Equal(t, tools.Ident("analytics.analyze"), chunks[2].ToolCallDelta.Name)
	assert.Equal(t, model.ChunkTypeToolCallDelta, chunks[3].Type)
	assert.Equal(t, model.ChunkTypeToolCall, chunks[4].Type)
	assert.Equal(t, "call_1", chunks[4].ToolCall.ID)
	assert.JSONEq(t, `{"query":"docs"}`, string(chunks[4].ToolCall.Payload))
	assert.Equal(t, model.ChunkTypeUsage, chunks[5].Type)
	assert.Equal(t, 15, chunks[5].UsageDelta.TotalTokens)
	assert.Equal(t, model.ChunkTypeStop, chunks[6].Type)
	assert.Equal(t, "tool_calls", chunks[6].StopReason)

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
	assert.Equal(t, model.ChunkTypeText, chunks[0].Type)
	assert.Equal(t, "Hello", chunks[0].Message.Parts[0].(model.TextPart).Text)
	assert.Equal(t, model.ChunkTypeUsage, chunks[1].Type)
	assert.Equal(t, 15, chunks[1].UsageDelta.TotalTokens)
	assert.Equal(t, model.ChunkTypeStop, chunks[2].Type)
	assert.Equal(t, "max_output_tokens", chunks[2].StopReason)
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
			Schema: []byte(`{"type":"object"}`),
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
	assert.Equal(t, model.ChunkTypeCompletionDelta, chunks[0].Type)
	assert.Equal(t, "draft_from_transcript", chunks[0].CompletionDelta.Name)
	assert.JSONEq(t, `{"answer":"ok"}`, chunks[0].CompletionDelta.Delta)
	assert.Equal(t, model.ChunkTypeCompletion, chunks[1].Type)
	assert.Equal(t, "draft_from_transcript", chunks[1].Completion.Name)
	assert.JSONEq(t, `{"answer":"ok"}`, string(chunks[1].Completion.Payload))
	assert.Equal(t, model.ChunkTypeStop, chunks[2].Type)
	assert.Equal(t, "stop", chunks[2].StopReason)
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

type stubLedgerSource struct {
	runID    string
	messages []*model.Message
	err      error
}

func (s *stubLedgerSource) Messages(_ context.Context, runID string) ([]*model.Message, error) {
	s.runID = runID
	return s.messages, s.err
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

func mustStreamEvent(t *testing.T, raw string) responses.ResponseStreamEventUnion {
	t.Helper()
	var event responses.ResponseStreamEventUnion
	require.NoError(t, json.Unmarshal([]byte(raw), &event))
	return event
}
