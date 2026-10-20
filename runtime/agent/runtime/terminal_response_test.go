// Typed terminal results retain the exact provider message through validation,
// journal selection and transcript publication without another terminal signal.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestTypedTerminalObserverPreservesProviderMessage(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		for _, mutation := range []string{"unchanged", "content", "metadata"} {
			t.Run(map[bool]string{false: "complete", true: "stream"}[streamed]+"/"+mutation, func(t *testing.T) {
				journal := &modelInvocationJournal{}
				response := testModelResponse([]model.Message{{Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.ThinkingPart{Text: "reason", Signature: "provider-signature", Final: true}, model.TextPart{Text: "answer"}},
					Meta:  map[string]any{"provider": "original"},
				}})
				client := newTestDesignatedModelInvocationClient(stubModelClient{
					complete: func(context.Context, *model.Request) (*model.Response, error) { return response, nil },
					stream: func(context.Context, *model.Request) (model.Streamer, error) {
						return &chunkStreamer{chunks: []model.Chunk{model.ThinkingChunk{Message: model.Message{Role: model.ConversationRoleAssistant, Parts: response.Content[0].Parts[:1]}}, model.TextChunk{Message: model.Message{Role: model.ConversationRoleAssistant, Parts: response.Content[0].Parts[1:]}}, model.StopChunk{Reason: response.StopReason}}, response: response}, nil
					},
				}, journal)
				var final *planner.FinalResponse
				if streamed {
					stream, err := client.Stream(t.Context(), &model.Request{})
					require.NoError(t, err)
					summary, err := planner.ConsumeStream(t.Context(), stream)
					require.NoError(t, err)
					final = summary.FinalResponse()
				} else {
					completed, err := client.Complete(t.Context(), &model.Request{})
					require.NoError(t, err)
					final = &planner.FinalResponse{Message: &completed.Content[0]}
				}
				require.NotNil(t, final)
				if mutation == "content" {
					final.Message.Parts = []model.Part{model.TextPart{Text: "changed"}}
				}
				if mutation == "metadata" {
					final.Message.Meta["provider"] = "changed"
				}
				result := &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{FinalResponse: final, Result: rawjson.Message(`{"value":1}`)}}
				transcript, err := journal.exportModelInvocation(result)
				if mutation != "unchanged" {
					require.ErrorContains(t, err, "modified provider-owned message")
					return
				}
				require.NoError(t, err)
				require.Len(t, transcript, 1)
				assert.Equal(t, transcript[0], result.FinalToolResult.FinalResponse.Message)
				require.Len(t, transcript[0].Parts, 2)
				assert.Equal(t, "provider-signature", transcript[0].Parts[0].(model.ThinkingPart).Signature)
				rt := New(newTestStore())
				base := &workflowConversation{RunContext: run.Context{RunID: "observer-run"}, finalization: &workflowFinalizationState{}}
				runtimeResult := &PlanResult{FinalToolResult: result.FinalToolResult}
				require.NoError(t, rt.appendSelectedModelResponse(t.Context(), "agent", base, "turn", testPublicationBatchID, runtimeResult, transcript))
				require.Len(t, base.Messages, 1)
				output, err := rt.materializeTerminalPlannerResult(t.Context(), &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}}, base, "turn", terminalPlannerState{result: runtimeResult})
				require.NoError(t, err)
				assert.Equal(t, result.FinalToolResult.FinalResponse.Message, output.Final)
				require.NotNil(t, output.FinalToolResult)
				require.NoError(t, validateWorkflowOutput(output, output.Subject, output.RunID))
				converter := workflowcodec.NewDataConverter()
				payload, err := converter.ToPayload(output)
				require.NoError(t, err)
				var replayed RunOutput
				require.NoError(t, converter.FromPayload(payload, &replayed))
				require.NoError(t, validateWorkflowOutput(&replayed, output.Subject, output.RunID))
				assert.Equal(t, output.Final.Parts, replayed.Final.Parts)
				assert.Equal(t, output.FinalToolResult.Result, replayed.FinalToolResult.Result)
				require.Len(t, base.Messages, 1)
			})
		}
	}
}

func TestTypedTerminalObserverKeepsExclusiveAndTypedContracts(t *testing.T) {
	resolution := testNativeRegistryResolution("revision/1")
	resolved, err := registrycontract.Resolve(resolution)
	require.NoError(t, err)
	spec := resolved.Specs[tools.Ident(resolution.Toolset.Tools[0].Name)]
	rt := New(newTestStore())
	for _, variant := range []string{"typed only", "observer", "both terminal branches", "invalid typed result", "invalid message"} {
		t.Run(variant, func(t *testing.T) {
			result := &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":1}`)}}
			if variant != "typed only" {
				result.FinalToolResult.FinalResponse = &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "answer"}}}}
			}
			switch variant {
			case "both terminal branches":
				result.FinalResponse = result.FinalToolResult.FinalResponse
			case "invalid typed result":
				result.FinalToolResult.Result = rawjson.Message(`[]`)
			case "invalid message":
				result.FinalToolResult.FinalResponse.Message = nil
			}
			err := validatePlannerActivityResult(rt, rt, result, &spec, false)
			if variant == "typed only" || variant == "observer" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if variant == "both terminal branches" {
				_, err := normalizeStepWithSpecs(&PlanResult{FinalResponse: result.FinalResponse, FinalToolResult: result.FinalToolResult}, rt)
				require.Error(t, err)
				var contractError *planner.OutputContractError
				require.ErrorAs(t, err, &contractError)
			}
		})
	}
}
