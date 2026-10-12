// These tests exercise context stops through serialized workflow activities,
// real model clients and the existing compactor. Stopped tools never run, live
// text remains in history, and recovery cannot repeat without progress.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type contextWindowTestProvider struct {
	requests   []*model.Request
	withTool   bool
	rawClient  bool
	repeatStop bool
	stopReason string
}

func TestContextWindowRecoveryWorkflow(t *testing.T) {
	for _, rawClient := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, toolBatch := range []bool{false, true} {
				name := map[bool]string{false: "designated/", true: "raw/"}[rawClient] + map[bool]string{false: "unary", true: "stream"}[streaming] + "/" + map[bool]string{false: "text", true: "tools"}[toolBatch]
				t.Run(name, func(t *testing.T) {
					provider := &contextWindowTestProvider{withTool: toolBatch, rawClient: rawClient}
					h, sink, summary := contextWindowHarness(t, provider, streaming)
					initialHistory := []*model.Message{systemMsg(), userMsg(strings.Repeat("older evidence ", 400)), assistantTextMsg("older answer"), userMsg("continue the current task")}
					h.base.HistoryEndID = testToolHistory(t, h.runtime, h.input.AgentID, h.base.RunContext, initialHistory)
					out, err := h.run(contextWindowKickoff(), initialCaps(RunPolicy{MaxToolCalls: 4, MaxRecoveryTurns: 2}))
					require.NoError(t, err)
					assert.Equal(t, "continued answer", out.Final.Text())
					require.Len(t, provider.requests, 2)
					assert.NotNil(t, summary.summarized)
					assert.Contains(t, textPart(t, provider.requests[1].Messages[1]), "[Conversation Summary]")
					assert.Equal(t, textPart(t, initialHistory[0]), textPart(t, provider.requests[1].Messages[0]))
					assert.Contains(t, strings.Join(canonicalHistory(t, provider.requests[1].Messages), "\n"), "continue the current task")
					require.NotNil(t, out.Usage)
					assert.Equal(t, 24, out.Usage.TotalTokens)
					assert.Equal(t, 200, out.Usage.CacheReadTokens)
					assert.Equal(t, 400, out.Usage.CacheWriteTokens)
					saved, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), h.runtime.Store, h.input.RunID, h.base.HistoryEndID)
					require.NoError(t, err)
					assert.Equal(t, canonicalHistory(t, initialHistory), canonicalHistory(t, saved[:len(initialHistory)]))
					assert.NotContains(t, strings.Join(canonicalHistory(t, saved), "\n"), "stopped-call")
					if streaming && !rawClient {
						assert.Contains(t, strings.Join(canonicalHistory(t, saved), "\n"), "Partial answer ")
						var replies string
						for _, event := range sink.snapshot() {
							if reply, ok := event.(stream.AssistantReply); ok {
								replies += reply.Data.Text
							}
						}
						assert.Equal(t, "Partial answer continued answer", replies)
					}
				})
			}
		}
	}
}

func TestContextWindowRecoveryStopsWhenAlreadyCompacted(t *testing.T) {
	provider := &contextWindowTestProvider{repeatStop: true}
	h, _, _ := contextWindowHarness(t, provider, false)
	h.base.HistoryEndID = testToolHistory(t, h.runtime, h.input.AgentID, h.base.RunContext, []*model.Message{userMsg(strings.Repeat("older evidence ", 400)), assistantTextMsg("older answer"), userMsg("newest question")})
	_, err := h.run(contextWindowKickoff(), initialCaps(RunPolicy{MaxToolCalls: 4, MaxRecoveryTurns: 3}))
	require.ErrorContains(t, err, "cannot compact history further")
	assert.Len(t, provider.requests, 2, "no third inference without more history to compact")
}

func TestContextWindowRecoveryLeavesMaximumOutputStopUnchanged(t *testing.T) {
	provider := &contextWindowTestProvider{stopReason: "max_tokens"}
	h, _, summary := contextWindowHarness(t, provider, false)
	h.base.HistoryEndID = testToolHistory(t, h.runtime, h.input.AgentID, h.base.RunContext, []*model.Message{userMsg("older question"), assistantTextMsg("older answer"), userMsg("newest question")})
	out, err := h.run(contextWindowKickoff(), initialCaps(RunPolicy{MaxToolCalls: 4, MaxRecoveryTurns: 2}))
	require.NoError(t, err)
	assert.Equal(t, "Partial answer ", out.Final.Text())
	assert.Len(t, provider.requests, 1)
	assert.Nil(t, summary.summarized)
}

func TestContextWindowRecoveryRejectsAmbiguousVariant(t *testing.T) {
	require.NoError(t, validateModelInvocationRecovery(&ModelInvocationRecovery{ContextWindow: true}))
	assert.Error(t, validateModelInvocationRecovery(&ModelInvocationRecovery{ContextWindow: true, NoCallBodyCorrection: "other recovery"}))
}

// contextWindowHarness uses the ordinary runtime and serializes activity inputs
// and outputs so the compaction decision survives the workflow boundary.
func contextWindowHarness(t *testing.T, provider *contextWindowTestProvider, streaming bool) (*recoveryHarness, *recordingStreamSink, *historyCountingClient) {
	t.Helper()
	kickoff, lookup := newAnyJSONSpec("catalog.kickoff"), newAnyJSONSpec("catalog.lookup")
	h := newRecoveryHarness(t, "context-window", []tools.ToolSpec{kickoff, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			assert.Equal(t, kickoff.Name, call.Name, "a stopped lookup must never execute")
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
			if in.Finalize != nil {
				assert.Equal(t, planner.TerminationReasonRecoveryCap, in.Finalize.Reason)
				return finalPlannerResult("stopped after recovery cap"), nil
			}
			req := &model.Request{Messages: in.Messages, Tools: in.Agent.AdvertisedToolDefinitions(), Model: "context-test"}
			if provider.rawClient {
				client, ok := in.Agent.ModelClient("context-test")
				require.True(t, ok)
				var summary planner.StreamSummary
				if streaming {
					st, err := client.Stream(ctx, req)
					if err != nil {
						return nil, err
					}
					summary, err = planner.ConsumeStream(ctx, st)
					if err != nil {
						return nil, err
					}
				} else {
					response, err := client.Complete(ctx, req)
					if err != nil {
						return nil, err
					}
					for _, call := range response.ToolCalls() {
						summary.ToolCalls = append(summary.ToolCalls, planner.ToolRequest{Name: call.Name, ModelToolCallID: call.ID, Payload: call.Payload})
					}
					if len(summary.ToolCalls) == 0 {
						return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
					}
				}
				if len(summary.ToolCalls) > 0 {
					return &planner.PlanResult{ToolCalls: summary.ToolCalls}, nil
				}
				return &planner.PlanResult{FinalResponse: summary.FinalResponse()}, nil
			}
			client, ok := in.Agent.PlannerModelClient("context-test")
			require.True(t, ok)
			if streaming {
				summary, err := client.Stream(ctx, req)
				if err != nil {
					return nil, err
				}
				return &planner.PlanResult{FinalResponse: summary.FinalResponse()}, nil
			}
			response, err := client.Complete(ctx, req)
			if err != nil {
				return nil, err
			}
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
		})
	h.runtime.models["context-test"] = historyTestClient(t, provider)
	summary := &historyCountingClient{summaryText: "older evidence summarized; continue the current task"}
	h.registration.Policy.History = Compress(historyTestClient(t, summary), HistoryCompressionConfig{AllowEstimatedTokens: true, CompressAtMaxInputTokens: 100000, KeepMaxTurns: 12})
	h.runtime.agents[h.input.AgentID] = h.registration
	sink := &recordingStreamSink{}
	subscriber, err := stream.NewSubscriber(sink, stream.AgentDebugProfile())
	require.NoError(t, err)
	h.runtime.streamSubscriber = subscriber
	h.workflow.plannerRoutes["resume"] = func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		var decoded PlanActivityInput
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return nil, err
		}
		output, err := h.runtime.PlanResumeActivity(ctx, &decoded)
		if err != nil {
			return nil, err
		}
		encoded, err = json.Marshal(output)
		if err != nil {
			return nil, err
		}
		var restored PlanActivityOutput
		if err := json.Unmarshal(encoded, &restored); err != nil {
			return nil, err
		}
		return &restored, nil
	}
	return h, sink, summary
}

func contextWindowKickoff() *PlanResult {
	return &PlanResult{ToolCalls: []ToolCall{{Name: "catalog.kickoff", ToolCallID: "kickoff", Payload: rawjson.Message(`{}`)}}}
}

func (p *contextWindowTestProvider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	return (model.TokenEstimator{}).CountTokens(ctx, req)
}

func (p *contextWindowTestProvider) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	p.requests = append(p.requests, req)
	text, reason := "continued answer", "end_turn"
	if len(p.requests) == 1 || p.repeatStop {
		text, reason = "Partial answer ", contextWindowStopReason
		if p.stopReason != "" {
			reason = p.stopReason
		}
	}
	response := testModelResponseWithUsage([]model.Message{*assistantTextMsg(text)}, model.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12, CacheReadTokens: 100, CacheWriteTokens: 200})
	if p.withTool && reason == contextWindowStopReason {
		response.Content[0].Parts = append(response.Content[0].Parts, model.ToolUsePart{ID: "stopped-call", Name: "catalog.lookup", Input: rawjson.Message(`{}`)})
	}
	response.StopReason = reason
	response.OutputLimited = reason == contextWindowStopReason || reason == "max_tokens"
	return response, nil
}

func (p *contextWindowTestProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	response, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	text := response.Content[0].Parts[0].(model.TextPart).Text
	chunks := []model.Chunk{model.TextChunk{Message: *assistantTextMsg(text)}}
	for _, call := range response.ToolCalls() {
		chunks = append(chunks, model.ToolCallChunk{ToolCall: call})
	}
	chunks = append(chunks, model.UsageChunk{Usage: response.Usage}, model.StopChunk{Reason: response.StopReason, OutputLimited: response.OutputLimited})
	return &chunkStreamer{chunks: chunks, response: response}, nil
}

func TestContextWindowCompactionMustShortenInput(t *testing.T) {
	provider := &historyCountingClient{summaryText: strings.Repeat("oversized summary ", 400)}
	policy := requireHistoryCompaction(Compress(historyTestClient(t, provider), HistoryCompressionConfig{AllowEstimatedTokens: true, CompressAtMaxInputTokens: 100000, KeepMaxTurns: 12}))
	_, err := policy(t.Context(), &model.Request{Messages: []*model.Message{userMsg("old"), assistantTextMsg("answer"), userMsg("new")}}, model.TokenEstimator{}, nil)
	assert.ErrorContains(t, err, "did not reduce")
}

func TestContextWindowCompactionRejectsNonSummarizingPolicy(t *testing.T) {
	policy := requireHistoryCompaction(func(_ context.Context, req *model.Request, _ model.TokenCounter, _ *HistorySummary) (HistoryResult, error) {
		return HistoryResult{Messages: req.Messages}, nil
	})
	_, err := policy(t.Context(), &model.Request{Messages: []*model.Message{userMsg("question")}}, model.TokenEstimator{}, nil)
	assert.ErrorContains(t, err, "requires a summary")
}

func TestContextWindowRecoveryDoesNotHideIndependentFailure(t *testing.T) {
	j := &modelInvocationJournal{}
	client := newPlannerModelClient(newTestDesignatedModelInvocationClient(&contextWindowTestProvider{}, j), j)
	_, err := client.Complete(t.Context(), &model.Request{Messages: []*model.Message{userMsg("question")}})
	require.Error(t, err)
	_, recoveryErr := j.recoverableModelInvocationRecovery(errors.Join(err, errors.New("independent planner failure")))
	assert.ErrorContains(t, recoveryErr, "unrelated planner failure")
}

func TestContextWindowCompactionMeasuresPreparedPriorSummary(t *testing.T) {
	messages := []*model.Message{userMsg(strings.Repeat("old evidence ", 400)), assistantTextMsg("old answer"), userMsg("recent question"), assistantTextMsg("recent answer"), userMsg("newest question")}
	prior := &HistorySummary{SourceMessages: 2, ReplacedMessages: 2, PolicyFingerprint: "test-policy", Message: *assistantTextMsg("prior summary")}
	policy := requireHistoryCompaction(func(_ context.Context, req *model.Request, _ model.TokenCounter, _ *HistorySummary) (HistoryResult, error) {
		summary := &HistorySummary{SourceMessages: 4, ReplacedMessages: 4, PolicyFingerprint: "test-policy", Message: *assistantTextMsg(strings.Repeat("expanded summary ", 100))}
		prepared, err := historySummaryMessages(req.Messages, summary)
		return HistoryResult{Messages: prepared, Summary: summary}, err
	})
	_, err := policy(t.Context(), &model.Request{Messages: messages}, model.TokenEstimator{}, prior)
	assert.ErrorContains(t, err, "did not reduce")
}

func TestContextWindowCompactionRejectsStoppedSummary(t *testing.T) {
	_, err := renderHistorySummary(&model.Response{Content: []model.Message{*assistantTextMsg("incomplete summary")}, StopReason: contextWindowStopReason}, "")
	assert.ErrorContains(t, err, "history summary reached")
}

func TestContextWindowCompactionReusesCoveredEvidence(t *testing.T) {
	provider := &historyCountingClient{}
	messages := make([]*model.Message, 0, 12)
	for range 6 {
		messages = append(messages, userMsg("question"), assistantTextMsg("answer"))
	}
	request := &model.Request{Messages: messages}
	policy := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})
	first, err := policy(t.Context(), request, provider, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Summary)
	require.Less(t, first.Summary.ReplacedMessages, first.Summary.SourceMessages)
	provider.summarized = nil
	recovered, err := requireHistoryCompaction(policy)(t.Context(), request, provider, first.Summary)
	require.NoError(t, err)
	assert.Nil(t, provider.summarized, "already covered evidence needs no second summary call")
	assert.Equal(t, first.Summary.SourceMessages, recovered.Summary.SourceMessages)
	assert.Greater(t, recovered.Summary.ReplacedMessages, first.Summary.ReplacedMessages)
	assertExactHistory(t, messages[len(messages)-2:], recovered.Messages[len(recovered.Messages)-2:])
}

func TestContextWindowRecoveryHonorsExistingBudget(t *testing.T) {
	provider := &contextWindowTestProvider{repeatStop: true}
	h, _, _ := contextWindowHarness(t, provider, false)
	h.base.HistoryEndID = testToolHistory(t, h.runtime, h.input.AgentID, h.base.RunContext, []*model.Message{userMsg(strings.Repeat("old evidence ", 400)), assistantTextMsg("old answer"), userMsg("new question")})
	out, err := h.run(contextWindowKickoff(), initialCaps(RunPolicy{MaxToolCalls: 4, MaxRecoveryTurns: 1}))
	require.NoError(t, err)
	assert.Equal(t, "stopped after recovery cap", out.Final.Text())
	assert.Len(t, provider.requests, 2)
}

func TestContextWindowRecoveryRequiresHistoryPolicy(t *testing.T) {
	provider := &contextWindowTestProvider{}
	h, _, _ := contextWindowHarness(t, provider, false)
	h.registration.Policy.History = nil
	h.runtime.agents[h.input.AgentID] = h.registration
	h.base.HistoryEndID = testToolHistory(t, h.runtime, h.input.AgentID, h.base.RunContext, []*model.Message{userMsg("question")})
	_, err := h.run(contextWindowKickoff(), initialCaps(RunPolicy{MaxToolCalls: 4, MaxRecoveryTurns: 1}))
	require.ErrorContains(t, err, "requires a history policy")
	assert.Len(t, provider.requests, 1)
}
