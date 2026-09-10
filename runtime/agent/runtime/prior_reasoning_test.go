package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	modelopenai "goa.design/goa-ai/features/model/openai"
	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestWithoutPriorReasoningPreparesExactInitialContext(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		for _, exclude := range []bool{false, true} {
			name := "session/default"
			if oneShot {
				name = "one-shot/default"
			}
			if exclude {
				name += "/without-prior"
			}
			t.Run(name, func(t *testing.T) {
				eng := &stubEngine{}
				client, store := newPreparedRunTestClient(eng, testAgentDefinition("svc.agent", "agent.workflow", "q", nil, nil))
				require.NoError(t, createPreparedRunSession(t.Context(), store))
				messages := priorReasoningFixture()
				original, err := model.CloneMessages(messages)
				require.NoError(t, err)
				opts := []RunOption{WithRunID("run-1")}
				if exclude {
					opts = append(opts, WithoutPriorReasoning())
				}
				var prepared *PreparedRun
				if oneShot {
					prepared, err = client.PrepareOneShot(messages, opts...)
				} else {
					prepared, err = client.Prepare("session-1", messages, opts...)
				}
				require.NoError(t, err)
				require.Equal(t, original, messages)
				require.Zero(t, eng.startCalls)
				data, err := prepared.MarshalBinary()
				require.NoError(t, err)
				parsed, err := ParsePreparedRun(data)
				require.NoError(t, err)
				messages[0].Parts[0] = model.TextPart{Text: "caller mutation"}
				_, err = client.StartPrepared(t.Context(), parsed)
				require.NoError(t, err)
				expected := original
				if exclude {
					expected, err = completedTurnMessages(original)
					require.NoError(t, err)
				}
				wantJSON, err := transcript.EncodeRunLogDelta(expected)
				require.NoError(t, err)
				gotJSON, err := transcript.EncodeRunLogDelta(eng.last.Input.Messages)
				require.NoError(t, err)
				require.Equal(t, wantJSON, gotJSON)
				// This policy is consumed before serialization, not a flag that
				// could remove reasoning generated later in the workflow.
				require.NotContains(t, string(data), "withoutPriorReasoning")
				again, err := parsed.MarshalBinary()
				require.NoError(t, err)
				require.Equal(t, data, again)
			})
		}
	}
}

func TestWithoutPriorReasoningAllowsForeignThinkingAtResponsesBoundary(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done.","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		assert.NoError(t, err)
	}))
	defer server.Close()
	sdk := openaisdk.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL))
	client, err := modelopenai.New(modelopenai.Options{Client: &sdk.Responses, DefaultModel: "gpt-test"})
	require.NoError(t, err)
	messages := priorReasoningFixture()
	messages = messages[:len(messages)-1]
	_, err = client.Complete(t.Context(), &model.Request{Messages: messages})
	require.ErrorContains(t, err, "thinking replay requires provider reasoning metadata")
	require.Empty(t, requests)
	start, err := buildOneShotRunStart("svc.agent", messages, []RunOption{WithoutPriorReasoning()})
	require.NoError(t, err)
	_, err = client.Complete(t.Context(), &model.Request{Messages: start.input.Messages})
	require.NoError(t, err)
	require.Len(t, requests, 1)
	// Preserving unrelated metadata does not relax the canonical requirement
	// that a model-request message contain content.
	metadataOnly, err := completedTurnMessages([]*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.ThinkingPart{Text: "private", Signature: "signed", Final: true}},
		Meta:  map[string]any{"application": "keep"},
	}})
	require.NoError(t, err)
	_, err = client.Complete(t.Context(), &model.Request{Messages: metadataOnly})
	require.ErrorContains(t, err, "message has no parts")
	require.Len(t, requests, 1)
	encoded, err := json.Marshal(requests[0]["input"])
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"call_id":"call-1"`)
	require.Contains(t, string(encoded), `"type":"function_call_output"`)
	require.Contains(t, string(encoded), "Found the report.")
	require.NotContains(t, string(encoded), "private reasoning")
	// A provider's unsupported citation format is not erased by the option.
	start.input.Messages = append(start.input.Messages, &model.Message{Role: model.ConversationRoleAssistant,
		Parts: []model.Part{model.CitationsPart{Text: "Cited answer"}}})
	_, err = client.Complete(t.Context(), &model.Request{Messages: start.input.Messages})
	require.ErrorContains(t, err, "canonical citations requires provider output metadata")
	require.Len(t, requests, 1)
}

func priorReasoningFixture() []*model.Message {
	return []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Find the report."}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Text: "private reasoning", Signature: "signed", Final: true},
			model.ThinkingPart{Redacted: []byte("opaque"), Final: true},
			model.TextPart{Text: "I will search."},
			model.ToolUsePart{ID: "call-1", Name: "reports.find", Input: rawjson.Message(`{"query":"revenue"}`), ThoughtSignature: "opaque-tool-signature"},
		}, Meta: map[string]any{"application": "keep"}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{ToolUseID: "call-1", Content: "Found the report."}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Here is the report."}}},
		{Role: model.ConversationRoleAssistant, Meta: map[string]any{modelmetadata.OpenAIReasoningItems: []string{`{"type":"reasoning","encrypted_content":"opaque"}`}}},
	}
}
