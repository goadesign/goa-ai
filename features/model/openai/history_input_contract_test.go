package openai

// Historical tool arguments and their provider metadata keep the accepted
// meaning even when the currently advertised input schema has narrowed.

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestHistoricalInputSurvivesNarrowerCurrentContract(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "native metadata"}[native], func(t *testing.T) {
			const canonical = `{"query":"status","obsolete":true}`
			const original = ` {"query":"status","obsolete":true,"optional":null} `
			message := &model.Message{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{model.ToolUsePart{
					ID: "call_1", Name: "svc.lookup", Input: rawjson.Message(canonical),
				}},
			}
			if native {
				item, err := json.Marshal(map[string]any{
					"type": "function_call", "id": "fc_1", "call_id": "call_1",
					"name": "svc_lookup", "arguments": original, "status": "completed",
				})
				require.NoError(t, err)
				message.Meta = map[string]any{
					openAIFunctionCallItemMetaKey:    string(item),
					openAIFunctionCallVersionMetaKey: openAIFunctionCallMetadataVersion2,
					openAIFunctionCallPayloadMetaKey: canonical,
				}
			}
			before, err := json.Marshal(message)
			require.NoError(t, err)
			transport := &mockTransport{completeResponse: mustCompletedResponse(t)}
			client, err := New(Options{DefaultModel: "test-model", transport: transport})
			require.NoError(t, err)
			request := &model.Request{
				Messages: []*model.Message{message, {
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.ToolResultPart{ToolUseID: "call_1", Content: "found"}},
				}},
				Tools: []*model.ToolDefinition{{
					Name: "svc.lookup", Description: "Look up a value.",
					Input: mustOpenAIToolInput(rawjson.Message(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)),
				}},
			}
			_, err = client.Complete(t.Context(), request)
			require.NoError(t, err)
			call := transport.completeRequests[0].Input.OfInputItemList[0].OfFunctionCall
			require.NotNil(t, call)
			if native {
				require.True(t, bytes.Equal([]byte(original), []byte(call.Arguments)), "provider arguments must remain byte-identical")
			} else {
				require.True(t, bytes.Equal([]byte(canonical), []byte(call.Arguments)), "canonical arguments must remain byte-identical")
			}
			after, err := json.Marshal(message)
			require.NoError(t, err)
			require.Equal(t, before, after)

			transport.completeResponse = mustResponse(t, `{
				"status":"completed",
				"output":[{"type":"function_call","id":"fc_2","call_id":"call_2",
				"name":"svc_lookup","arguments":"{\"query\":\"status\",\"obsolete\":true}"}]
			}`)
			_, err = client.Complete(t.Context(), request)
			var invalid *model.OutputValidationError
			require.ErrorAs(t, err, &invalid, "new calls must satisfy the current advertised schema")
		})
	}
}
