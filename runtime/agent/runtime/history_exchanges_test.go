// These tests exercise complete multi-message responses through the real history
// policies and planner activities. Synthetic providers validate every counted
// and delivered transcript; no model or external service is called.
package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	// exchangeHistoryProvider checks the real transcript contract before the
	// scripted counter supplies its exact synthetic cost.
	exchangeHistoryProvider struct {
		*fitProvider
		t *testing.T
	}
)

func TestHistoryPoliciesKeepCompleteResponseExchanges(t *testing.T) {
	for _, pendingUser := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending_user_%t", pendingUser), func(t *testing.T) {
			messages, exchanges := completeExchangeHistory(3)
			if pendingUser {
				pending := userMsg("A new request, not a tool result")
				messages = append(messages, pending)
				exchanges = append(exchanges, []*model.Message{pending})
			}
			before := canonicalHistory(t, messages)
			require.NoError(t, transcript.ValidatePlannerTranscript(messages))
			turns := parseTurns(messages[1:])
			require.Len(t, turns, len(exchanges))
			assert.Same(t, messages[1], turns[0].messages[0], "kickoff stays with the first response")
			for keep := 1; keep <= len(exchanges); keep++ {
				out, err := KeepRecentTurns(keep)(t.Context(), messages, nil)
				require.NoError(t, err)
				require.NoError(t, transcript.ValidatePlannerTranscript(out))
				want := []*model.Message{messages[0]}
				if keep == len(exchanges) {
					want = append(want, messages[1])
				}
				for _, exchange := range exchanges[len(exchanges)-keep:] {
					want = append(want, exchange...)
				}
				assertExactHistory(t, want, out)
			}
			assert.Equal(t, before, canonicalHistory(t, messages))
		})
	}
}

func TestHistoryGroupsTextOnlyResponsesAndOrdinaryRequests(t *testing.T) {
	messages := []*model.Message{
		systemMsg(), userMsg("First question"), assistantTextMsg("First fragment"),
		assistantTextMsg("Second fragment"), userMsg("Next question"),
		assistantTextMsg("Next answer"), userMsg("Pending question"),
	}
	turns := parseTurns(messages[1:])
	require.Len(t, turns, 3)
	assertExactHistory(t, messages[1:4], turns[0].messages)
	assertExactHistory(t, messages[4:6], turns[1].messages)
	assertExactHistory(t, messages[6:], turns[2].messages)
	out, err := KeepRecentTurns(2)(t.Context(), messages, nil)
	require.NoError(t, err)
	assertExactHistory(t, append([]*model.Message{messages[0]}, messages[4:]...), out)
}

func TestCompressCompleteExchangesAtEverySelectionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config HistoryCompressionConfig
		counts []fitCount
	}{
		{
			name:   "turn trigger and retention",
			config: HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 2},
		},
		{
			name:   "older token allowance",
			config: HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 3, KeepMaxInputTokens: 60},
			counts: []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 161}},
		},
		{
			name:   "actual summary shortens eligible suffix",
			config: HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 3, CompressAtMaxInputTokens: 200},
			counts: []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}, {tokens: 201}, {tokens: 200}},
		},
		{
			name:   "token trigger and final fit",
			config: HistoryCompressionConfig{KeepMaxTurns: 3, CompressAtMaxInputTokens: 200},
			counts: []fitCount{{tokens: 201}, {tokens: 100}, {tokens: 160}, {tokens: 190}, {tokens: 201}, {tokens: 200}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages, exchanges := completeExchangeHistory(4)
			before := canonicalHistory(t, messages)
			provider := newExchangeHistoryProvider(t, tc.counts)
			out, err := Compress(historyTestClient(t, provider), tc.config)(t.Context(), messages, fitTools())
			require.NoError(t, err)
			require.NoError(t, transcript.ValidatePlannerTranscript(out))
			assertExactHistory(t, append(exchanges[2], exchanges[3]...), out[2:])
			assert.Same(t, messages[0], out[0])
			assert.Equal(t, 1, provider.completeCalls)
			assert.Len(t, provider.requests, len(tc.counts))
			summaryInput := textPart(t, provider.request.Messages[1])
			for _, exchange := range []int{0, 1} {
				for call := range 7 {
					id := fmt.Sprintf("exchange_%d_call_%d", exchange, call)
					assert.Contains(t, summaryInput, `"id":"`+id+`"`)
					assert.Contains(t, summaryInput, `"tool_use_id":"`+id+`"`)
				}
			}
			assert.NotContains(t, summaryInput, "exchange_3_call_")
			assert.Equal(t, before, canonicalHistory(t, messages))
		})
	}
}

func TestCompressRejectsOversizedCompleteNewestExchange(t *testing.T) {
	messages, _ := completeExchangeHistory(3)
	provider := newExchangeHistoryProvider(t, []fitCount{{tokens: 201}})
	out, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{
		CompressAtTurns: 3, KeepMaxTurns: 1, CompressAtMaxInputTokens: 200,
	})(t.Context(), messages, fitTools())
	require.ErrorContains(t, err, "newest history turn cannot fit")
	assertExactHistory(t, messages, out)
	assert.Zero(t, provider.completeCalls)
	require.Len(t, provider.requests, 1)
}

func TestPlanActivitiesPrepareCompleteResponseExchanges(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume_%t", resume), func(t *testing.T) {
			messages, exchanges := completeExchangeHistory(4)
			before := canonicalHistory(t, messages)
			provider := newExchangeHistoryProvider(t, nil)
			modelCalls := 0
			plan := func(ctx context.Context, prepare func() ([]*model.Message, error), agentCtx planner.PlannerContext) (*planner.PlanResult, error) {
				prepared, err := prepare()
				if err != nil {
					return nil, err
				}
				again, err := prepare()
				require.NoError(t, err)
				assert.Same(t, &prepared[0], &again[0])
				require.NoError(t, transcript.ValidatePlannerTranscript(prepared))
				assertExactHistory(t, append(exchanges[2], exchanges[3]...), prepared[2:])
				client, ok := agentCtx.PlannerModelClient("test")
				require.True(t, ok)
				response, err := client.Complete(ctx, &model.Request{Model: "test", Messages: prepared})
				if err != nil {
					return nil, err
				}
				return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
			}
			pl := &stubPlanner{
				start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
					return plan(ctx, in.PrepareMessages, in.Agent)
				},
				resume: func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
					return plan(ctx, in.PrepareMessages, in.Agent)
				},
			}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			reg := rt.agents["service.agent"]
			reg.Policy.History = Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 2})
			rt.agents["service.agent"] = reg
			rt.models["test"] = mustTestModelClient(stubModelClient{
				complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
					modelCalls++
					require.NoError(t, transcript.ValidatePlannerTranscript(request.Messages))
					// Model request admission clones messages; history preparation
					// preserves pointers, while the provider receives equal values.
					assert.Equal(t, canonicalHistory(t, append(exchanges[2], exchanges[3]...)), canonicalHistory(t, request.Messages[2:]))
					return &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Complete"}}}}, StopReason: "stop"}, nil
				},
			})
			call := rt.PlanStartActivity
			if resume {
				call = rt.PlanResumeActivity
			}
			out, err := call(t.Context(), &PlanActivityInput{
				AgentID: "service.agent", RunID: "synthetic-run", RunContext: run.Context{RunID: "synthetic-run"}, Messages: messages,
			})
			require.NoError(t, err)
			require.NotNil(t, out.Result.FinalResponse)
			assert.Equal(t, 1, modelCalls)
			assert.Equal(t, 1, provider.completeCalls)
			assert.Equal(t, before, canonicalHistory(t, messages))
		})
	}
}

// completeExchangeHistory models one kickoff followed by independently complete
// responses. Each response has separate reasoning, text, and seven call messages,
// then a result message with accompanying text and a result reminder.
func completeExchangeHistory(count int) ([]*model.Message, [][]*model.Message) {
	const parallelCalls = 7
	const exchangeMessages = parallelCalls + 4 // reasoning, text, results, reminder
	messages := make([]*model.Message, 2, 2+exchangeMessages*count)
	messages[0], messages[1] = systemMsg(), userMsg("Complete the requested work")
	exchanges := make([][]*model.Message, count)
	for i := range count {
		exchange := make([]*model.Message, 2, exchangeMessages)
		exchange[0] = &model.Message{Role: model.ConversationRoleAssistant, Meta: map[string]any{"provider_replay": "preserved"}, Parts: []model.Part{model.ThinkingPart{Text: "Consider the next reads", Signature: "synthetic-signature", Final: true}}}
		exchange[1] = assistantTextMsg("Reading the next sources")
		results := &model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Results for the complete response"}}}
		for j := range parallelCalls {
			id := fmt.Sprintf("exchange_%d_call_%d", i, j)
			exchange = append(exchange, &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{
				model.ToolUsePart{ID: id, Name: "lookup", Input: rawjson.Message(`{"id":"synthetic-source"}`), ThoughtSignature: "synthetic-call-signature"},
			}})
			results.Parts = append(results.Parts, model.ToolResultPart{ToolUseID: id, Content: rawjson.Message(`{"value":9007199254740993}`)})
		}
		exchange = append(exchange, results, &model.Message{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "Interpret the preceding results"}}})
		exchanges[i] = exchange
		messages = append(messages, exchange...)
	}
	return messages, exchanges
}

func newExchangeHistoryProvider(t *testing.T, counts []fitCount) *exchangeHistoryProvider {
	t.Helper()
	return &exchangeHistoryProvider{t: t, fitProvider: &fitProvider{
		evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Older complete exchanges"}), counts: counts,
	}}
}

func assertExactHistory(t *testing.T, want, actual []*model.Message) {
	t.Helper()
	require.Len(t, actual, len(want))
	for i := range want {
		assert.Same(t, want[i], actual[i])
	}
	assert.Equal(t, canonicalHistory(t, want), canonicalHistory(t, actual))
}

func (p *exchangeHistoryProvider) CountTokens(ctx context.Context, request *model.Request) (model.TokenCount, error) {
	require.NoError(p.t, transcript.ValidatePlannerTranscript(request.Messages), "every candidate must contain complete response/result exchanges")
	return p.fitProvider.CountTokens(ctx, request)
}
