// These tests carry ordered tool content across activity, child, provided-result,
// saved-event and model-history boundaries. Structured result codecs remain the
// authority for domain JSON; content remains an independent typed value.
package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
	"goa.design/goa-ai/runtime/content"
)

func TestToolContentCrossesActivityChildAndSavedResultBoundaries(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			spec := newAnyJSONSpec("remote.lookup")
			rt := New(newTestStore())
			seedTestToolSpecs(rt, spec)
			call := ToolCall{Name: spec.Name, ToolCallID: "runtime-call", ModelToolCallID: "provider-call", RunID: "call-run", SessionID: "session-1", AgentID: "service.agent"}
			result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"answer": "accepted"}, Blocks: richToolContentForTest()}
			if failed {
				result.Result = nil
				result.Failure = testToolFailure(planner.FailureDomainRejection, planner.RecoveryReplan, "request rejected")
			}
			want := result.Blocks.Clone()
			out, err := executeRichContentActivityForTest(t, rt, call, result)
			require.NoError(t, err)
			copied, err := workflowcodec.Copy(workflowcodec.NewDataConverter(), out)
			require.NoError(t, err)
			assert.Equal(t, want, copied.Blocks)
			out.Blocks[0].(*content.TextContent).Text = "activity changed"
			assert.Equal(t, want, copied.Blocks)

			event := &api.ToolEvent{Name: call.Name, ToolCallID: call.ToolCallID, Result: copied.Payload, Blocks: copied.Blocks, Failure: copied.Failure}
			child, err := rt.decodeAgentChildFinalToolResult(&call, event)
			require.NoError(t, err)
			assert.Equal(t, want, child.Blocks)
			assert.Equal(t, failed, child.Failure != nil)
			event.Blocks[0].(*content.TextContent).Text = "child source changed"
			assert.Equal(t, want, child.Blocks)
			child.ToolCallID = call.ToolCallID
			base := &workflowConversation{RunContext: run.Context{RunID: "result-run", SessionID: call.SessionID}}
			record := materializedToolRecordForTest(t, rt, call.AgentID, base, call, child)
			child.Blocks[0].(*content.TextContent).Text = "executor changed after acceptance"
			part, err := toolResultRecordPart(record)
			require.NoError(t, err)
			assert.Equal(t, want, part.Blocks)
			assert.Equal(t, call.ModelToolCallID, part.ToolUseID)
			assert.Equal(t, failed, part.IsError)
			if failed {
				assert.Equal(t, "request rejected", part.Content)
			}
		})
	}
}

func TestContentOnlyResultsKeepDeclaredDomainContract(t *testing.T) {
	rt := New(newTestStore())
	noResult := newAnyJSONSpec("remote.reset")
	noResult.Result = tools.TypeSpec{}
	declared := newAnyJSONSpec("remote.lookup")
	seedTestToolSpecs(rt, noResult, declared)
	for _, spec := range []tools.ToolSpec{noResult, declared} {
		t.Run(string(spec.Name), func(t *testing.T) {
			call := ToolCall{Name: spec.Name, ToolCallID: "call-1"}
			result := &planner.ToolResult{Name: call.Name, Blocks: richToolContentForTest()}
			out, err := executeRichContentActivityForTest(t, rt, call, result)
			require.NoError(t, err)
			if spec.Name == noResult.Name {
				assert.Nil(t, out.Failure)
				assert.Empty(t, out.Payload)
				assert.Equal(t, richToolContentForTest(), out.Blocks)
			} else {
				require.NotNil(t, out.Failure)
				assert.Equal(t, planner.FailureMalformedResult, out.Failure.Kind)
				assert.Empty(t, out.Blocks)
			}
		})
	}
}

func TestToolContentRejectsInvalidBoundaryValues(t *testing.T) {
	rt := New(newTestStore())
	spec := newAnyJSONSpec("remote.lookup")
	seedTestToolSpecs(rt, spec)
	call := ToolCall{Name: spec.Name, ToolCallID: "call-1"}
	invalid := content.Blocks{&content.ImageContent{Data: "not base64", MIMEType: "image/png"}}
	_, err := rt.decodeAgentChildFinalToolResult(&call, &api.ToolEvent{Blocks: invalid, Result: rawjson.Message(`{}`)})
	require.ErrorContains(t, err, "agent-tool final content")
	_, _, err = rt.decodeProvidedToolResult(t.Context(), spec, call, &api.ProvidedToolResult{Blocks: invalid, Success: &api.ProvidedToolSuccess{Result: rawjson.Message(`{}`)}})
	require.ErrorContains(t, err, "invalid tool content")
	result := &planner.ToolResult{Name: call.Name, Result: map[string]any{}, Blocks: invalid}
	out, err := executeRichContentActivityForTest(t, rt, call, result)
	require.NoError(t, err)
	require.NotNil(t, out.Failure)
	assert.Equal(t, planner.FailureMalformedResult, out.Failure.Kind)
	assert.Empty(t, out.Blocks)
}

func TestPlannerOutputContentSharesAggregateBudget(t *testing.T) {
	small := &PlanActivityOutput{Result: &PlanResult{FinalToolResult: &planner.FinalToolResult{Blocks: richToolContentForTest()}}}
	require.NoError(t, checkPlanActivityOutputBudget(small))
	text := strings.Repeat("x", maxPlanActivityOutputBytes/2)
	for _, blocks := range []content.Blocks{{&content.TextContent{Text: text}}, {&content.ImageContent{Data: text, MIMEType: "image/png"}}} {
		small.Result.FinalToolResult.Blocks = blocks
		require.NoError(t, checkPlanActivityOutputBudget(small))
	}
	small.Result.FinalToolResult.Blocks = content.Blocks{&content.TextContent{Text: text}, &content.ImageContent{Data: text, MIMEType: "image/png"}}
	assert.ErrorContains(t, checkPlanActivityOutputBudget(small), "encoded-size bound")
}

func TestToolResultBatchCopiesContentIndependently(t *testing.T) {
	result := &planner.ToolResult{Blocks: richToolContentForTest()}
	copied := cloneToolResults([]*planner.ToolResult{result})
	copied[0].Blocks[0].(*content.TextContent).Text = "changed"
	assert.Equal(t, "accepted observation", result.Blocks[0].(*content.TextContent).Text)
}

func TestModelResultContentSurvivesSemanticOmission(t *testing.T) {
	rt := New(newTestStore())
	spec := newAnyJSONSpec("remote.lookup")
	seedTestToolSpecs(rt, spec)
	call := ToolCall{Name: spec.Name, ToolCallID: "call-1"}
	result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"blob": strings.Repeat("x", transcript.MaxToolResultContentBytes+1)}, Blocks: richToolContentForTest()}
	record := materializedToolRecordForTest(t, rt, "service.agent", &workflowConversation{RunContext: run.Context{RunID: "run-1"}}, call, result)
	part, err := toolResultRecordPart(record)
	require.NoError(t, err)
	assert.Equal(t, true, part.Content.(map[string]any)["omitted"])
	assert.Equal(t, richToolContentForTest(), part.Blocks)
	message, err := json.Marshal(&model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{part}})
	require.NoError(t, err)
	var restored model.Message
	require.NoError(t, json.Unmarshal(message, &restored))
	assert.Equal(t, part.Blocks, restored.Parts[0].(model.ToolResultPart).Blocks)
}

func TestProvidedResultsRetainContentOnSuccessAndFailure(t *testing.T) {
	rt := New(newTestStore())
	spec := newAnyJSONSpec("remote.lookup")
	seedTestToolSpecs(rt, spec)
	call := ToolCall{Name: spec.Name, ToolCallID: "call-1", Payload: rawjson.Message(`{}`)}
	for _, failed := range []bool{false, true} {
		provided := &api.ProvidedToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Blocks: richToolContentForTest()}
		if failed {
			provided.Failure = &api.ProvidedToolFailure{Kind: planner.FailureDomainRejection, Message: "rejected", Action: planner.RecoveryReplan}
		} else {
			provided.Success = &api.ProvidedToolSuccess{Result: rawjson.Message(`{"value":"accepted"}`)}
		}
		result, _, err := rt.decodeProvidedToolResult(t.Context(), spec, call, provided)
		require.NoError(t, err)
		assert.Equal(t, richToolContentForTest(), result.Blocks)
		assert.Equal(t, failed, result.Failure != nil)
		provided.Blocks[0].(*content.TextContent).Text = "changed"
		assert.Equal(t, "accepted observation", result.Blocks[0].(*content.TextContent).Text)
	}
}

func TestCheckpointRejectsContentDisagreementForAcceptedResult(t *testing.T) {
	rt := New(newTestStore())
	spec := newAnyJSONSpec("remote.lookup")
	seedTestToolSpecs(rt, spec)
	base := &workflowConversation{RunContext: run.Context{RunID: "run-1", SessionID: "session-1"}}
	call := ToolCall{Name: spec.Name, ToolCallID: "call-1"}
	result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"value": "accepted"}, Blocks: richToolContentForTest()}
	accepted := materializedToolRecordForTest(t, rt, "service.agent", base, call, result)
	event, err := encodeToolEvent(result, accepted.call, rt.toolSpec)
	require.NoError(t, err)
	record := checkpointToolRecord{Call: accepted.call, Result: event, CallRunID: accepted.callRunID, ResultRunID: accepted.resultRunID, ResultJSON: accepted.resultJSON, ResultPublished: true, ResultRecord: accepted.resultRecord}
	checkpoint := &workflowCheckpoint{AgentID: "service.agent", SessionID: "session-1"}
	definition := testAgentDefinition("service.agent", "service.workflow", "service.queue", []tools.ToolSpec{spec}, nil)
	require.NoError(t, validateCheckpointResultRecord(record, checkpoint, definition))
	event.Blocks[0].(*content.TextContent).Text = "different accepted content"
	assert.ErrorContains(t, validateCheckpointResultRecord(record, checkpoint, definition), "disagrees with batch result")
}

func TestSavedToolContentReplaysInModelTranscript(t *testing.T) {
	rt := New(newTestStore())
	spec := newAnyJSONSpec("remote.lookup")
	seedTestToolSpecs(rt, spec)
	input := &RunInput{AgentID: "service.agent", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &workflowConversation{RunContext: run.Context{RunID: input.RunID, SessionID: input.SessionID}}
	call := ToolCall{Name: spec.Name, ToolCallID: "runtime-call", ModelToolCallID: "provider-call", Payload: rawjson.Message(`{}`)}
	require.NoError(t, rt.appendTranscriptMessages(t.Context(), input.AgentID, base, input.TurnID, []*model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: call.ModelToolCallID, Name: string(call.Name), Input: call.Payload}}}}))
	result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"value": "accepted"}, Blocks: richToolContentForTest()}
	accepted := materializedToolRecordForTest(t, rt, input.AgentID, base, call, result)
	require.NoError(t, rt.appendUserToolRecordResults(t.Context(), input.AgentID, base, []stepToolRecord{accepted}, input.TurnID))
	messages, err := transcript.BuildMessagesFromRunLog(t.Context(), rt.Store, input.RunID)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	part := messages[1].Parts[0].(model.ToolResultPart)
	assert.Equal(t, "provider-call", part.ToolUseID)
	assert.Equal(t, richToolContentForTest(), part.Blocks)
	result.Blocks[0].(*content.TextContent).Text = "changed after recording"
	assert.Equal(t, richToolContentForTest(), part.Blocks)
}

// richToolContentForTest returns synthetic media and resource descriptions with
// nested metadata. No address is fetched by any test consumer.
func richToolContentForTest() content.Blocks {
	priority := 0.25
	mime := "text/plain"
	return content.Blocks{
		&content.TextContent{Text: "accepted observation", Meta: []byte(`{"sequence":9007199254740993}`), Annotations: &content.Annotations{Audience: []content.Role{content.RoleAssistant, content.RoleUser}, Priority: &priority}},
		&content.ImageContent{Data: "AQID", MIMEType: "image/png"},
		&content.AudioContent{Data: "BAUG", MIMEType: "audio/wav", Annotations: &content.Annotations{Audience: []content.Role{content.RoleUser}}},
		&content.ResourceLink{Name: "report", URI: "https://example.com/report", Icons: []content.Icon{{Src: "https://example.com/icon", Sizes: []string{"any"}}}},
		&content.EmbeddedResource{Resource: &content.TextResourceContents{URI: "report://accepted", MIMEType: &mime, Text: "report text"}},
	}
}

// executeRichContentActivityForTest invokes the production activity with a
// registered synthetic executor, including its materialization and byte checks.
func executeRichContentActivityForTest(t *testing.T, rt *Runtime, call ToolCall, result *planner.ToolResult) (*ToolOutput, error) {
	t.Helper()
	spec, ok := rt.toolSpec(call.Name)
	require.True(t, ok)
	seedTestToolset(rt, "remote", spec)
	binding := rt.toolsets["remote"]
	binding.Execute = func(context.Context, *ToolCall) (*ToolExecutionResult, error) { return Executed(result), nil }
	rt.toolsets["remote"] = binding
	return rt.ExecuteToolActivity(t.Context(), &ToolInput{ToolsetName: "remote", ToolName: call.Name, ToolCallID: call.ToolCallID, RunID: call.RunID, AgentID: call.AgentID, SessionID: call.SessionID, Payload: rawjson.Message(`{}`)})
}
