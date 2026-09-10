package runtime

// These tests use real planner activities and model observers. A summary must
// follow the exact chosen response, never whichever helper call finishes last.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestRequestHistoryActivityCarry(t *testing.T) {
	var fresh, reused int
	policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
		if prior == nil {
			fresh++
		} else {
			reused++
		}
		return requestHistoryResult(req.Messages, "saved history", prior)
	}
	pl := &stubPlanner{
		start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			return requestHistoryAnswer(ctx, in.Agent, in.Messages)
		},
		resume: func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return requestHistoryAnswer(ctx, in.Agent, in.Messages)
		},
	}
	rt := newRequestHistoryRuntime(pl, policy)
	messages := requestHistoryMessages()
	original := canonicalHistory(t, messages)
	input := &PlanActivityInput{AgentID: "service.agent", RunID: "history-run", RunContext: run.Context{RunID: "history-run"}, Messages: messages}
	out, err := rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, out.HistoryContext)
	assert.Equal(t, original, canonicalHistory(t, input.Messages))
	assert.Equal(t, "answer", out.Transcript[0].Parts[0].(model.TextPart).Text)
	input.Messages = append(input.Messages, out.Transcript...)
	input.Messages = append(input.Messages, userMsg("follow-up"))
	input.HistoryContext = out.HistoryContext
	out, err = rt.PlanResumeActivity(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, out.HistoryContext)
	assert.Equal(t, 1, fresh)
	assert.Equal(t, 1, reused)
	assert.Equal(t, original, canonicalHistory(t, input.Messages[:len(messages)]))
}

func TestRequestHistoryOnlySelectedConcurrentInvocationIsPromoted(t *testing.T) {
	policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
		return requestHistoryResult(req.Messages, req.Model, prior)
	}
	pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := in.Agent.ModelClient("test")
		require.True(t, ok)
		var responses [2]*model.Response
		var errs [2]error
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Go(func() {
				responses[i], errs[i] = client.Complete(ctx, &model.Request{Model: []string{"selected", "helper"}[i], Messages: in.Messages})
			})
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &responses[0].Content[0]}}, nil
	}}
	rt := newRequestHistoryRuntime(pl, policy)
	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{AgentID: "service.agent", RunID: "history-run", Messages: requestHistoryMessages()})
	require.NoError(t, err)
	require.NotNil(t, out.HistoryContext)
	assert.Equal(t, "selected", out.HistoryContext.Summary.Message.Parts[0].(model.TextPart).Text)
}

func TestRequestHistoryStreamPromotesItsOwnSummary(t *testing.T) {
	policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
		return requestHistoryResult(req.Messages, "stream history", prior)
	}
	pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := in.Agent.PlannerModelClient("test")
		require.True(t, ok)
		summary, err := client.Stream(ctx, &model.Request{Model: "test", Messages: in.Messages})
		if err != nil {
			return nil, err
		}
		return &planner.PlanResult{FinalResponse: summary.FinalResponse()}, nil
	}}
	rt := newRequestHistoryRuntime(pl, policy)
	rt.models["test"] = mustTestModelClient(stubModelClient{stream: func(context.Context, *model.Request) (model.Streamer, error) {
		response := testModelResponse([]model.Message{*assistantTextMsg("stream answer")})
		return &chunkStreamer{
			chunks: []model.Chunk{
				model.TextChunk{Message: response.Content[0]},
				model.StopChunk{Reason: response.StopReason},
			},
			response: response,
		}, nil
	}})
	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{AgentID: "service.agent", RunID: "history-run", Messages: requestHistoryMessages()})
	require.NoError(t, err)
	require.NotNil(t, out.HistoryContext)
	assert.Equal(t, "stream history", out.HistoryContext.Summary.Message.Parts[0].(model.TextPart).Text)
	assert.Equal(t, "stream answer", out.Transcript[0].Parts[0].(model.TextPart).Text)
}

func TestRequestHistoryCodeOnlyDoesNotPromoteHelper(t *testing.T) {
	for _, helper := range []bool{false, true} {
		t.Run(map[bool]string{false: "no model", true: "unselected helper"}[helper], func(t *testing.T) {
			calls := 0
			policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
				calls++
				return requestHistoryResult(req.Messages, "helper", prior)
			}
			pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
				if helper {
					client, ok := in.Agent.ModelClient("test")
					require.True(t, ok)
					if _, err := client.Complete(ctx, &model.Request{Model: "test", Messages: in.Messages}); err != nil {
						return nil, err
					}
				}
				return finalPlannerResult("finished without selecting a model response"), nil
			}}
			rt := newRequestHistoryRuntime(pl, policy)
			out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{AgentID: "service.agent", RunID: "history-run", Messages: requestHistoryMessages()})
			require.NoError(t, err)
			assert.Nil(t, out.HistoryContext)
			if helper {
				assert.Equal(t, 1, calls)
			} else {
				assert.Zero(t, calls)
			}
		})
	}
}

func TestRequestHistoryRecoveryPromotesExactInvocation(t *testing.T) {
	for _, kind := range []string{"answer", "planning", "tool validation"} {
		t.Run(kind, func(t *testing.T) {
			policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
				return requestHistoryResult(req.Messages, "recovery history", prior)
			}
			pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
				client, ok := in.Agent.PlannerModelClient("test")
				require.True(t, ok)
				resp, err := client.Complete(ctx, &model.Request{Model: "test", Messages: in.Messages, Tools: fitTools()})
				if err != nil {
					return nil, err
				}
				if kind == "planning" {
					return nil, planner.NewRecoverableModelPlanningError(errors.New("missing evidence"), &resp.Content[0], "Use the supplied evidence.")
				}
				return nil, planner.NewRecoverableModelAnswerError(errors.New("unsupported answer"), &planner.FinalResponse{Message: &resp.Content[0]}, "Use the supplied evidence.")
			}}
			rt := newRequestHistoryRuntime(pl, policy)
			if kind == "tool validation" {
				rt.models["test"] = mustTestModelClient(stubModelClient{complete: func(context.Context, *model.Request) (*model.Response, error) {
					return testModelResponse([]model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "unknown-call", Name: "catalog.unknown", Input: rawjson.Message(`{}`)}}}}), nil
				}})
			}
			out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{AgentID: "service.agent", RunID: "history-run", Messages: requestHistoryMessages()})
			require.NoError(t, err)
			require.NotNil(t, out)
			if kind == "tool validation" {
				require.NotNil(t, out.ModelInvocationRecovery)
			} else {
				require.NotNil(t, out.OutputContractFailure)
				require.NotNil(t, out.OutputContractFailure.ModelOutputRecovery)
			}
			require.NotNil(t, out.HistoryContext)
			assert.Equal(t, "recovery history", out.HistoryContext.Summary.Message.Parts[0].(model.TextPart).Text)
		})
	}
}

func TestRequestHistoryBindingsAndInvalidClaims(t *testing.T) {
	canonical := requestHistoryMessages()
	result, err := requestHistoryResult(canonical, "summary", nil)
	require.NoError(t, err)
	source, positions, _, err := historySourceHashes(canonical, 2)
	require.NoError(t, err)
	saved := &api.HistoryContext{Summary: *result.Summary, SourceSHA256: source, SourcePositionsSHA256: positions}
	require.NoError(t, validateHistoryContext(canonical, saved))
	for _, test := range []struct {
		name     string
		messages []*model.Message
		eligible bool
	}{
		{"same source", canonical, true},
		{"changed source", []*model.Message{systemMsg(), userMsg("changed"), canonical[2], canonical[3]}, false},
		{"shifted source positions", append([]*model.Message{systemMsg()}, canonical...), false},
		{"changed current instruction", []*model.Message{historySystemMessage("new current instruction"), canonical[1], canonical[2], canonical[3]}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
				assert.Equal(t, test.eligible, prior != nil)
				return HistoryResult{Messages: req.Messages}, nil
			}
			_, _, err := prepareRequestHistory(t.Context(), &model.Request{Messages: test.messages}, nil, policy, canonical, saved)
			require.NoError(t, err)
		})
	}
	invalid := *saved
	invalid.SourceSHA256 = "incorrect"
	require.Error(t, validateHistoryContext(canonical, &invalid))
	invalid = *saved
	invalid.Summary.SourceMessages = 1
	require.Error(t, validateHistoryContext(canonical, &invalid))
	invalid = *saved
	invalid.Summary.Message = model.Message{Role: model.ConversationRoleSystem}
	require.ErrorContains(t, validateHistoryContext(canonical, &invalid), "summary message")
	invalid.Summary.Message = model.Message{Role: "invalid", Parts: []model.Part{model.TextPart{Text: "summary"}}}
	require.ErrorContains(t, validateHistoryContext(canonical, &invalid), "summary message")
	invalidPolicy := func(context.Context, *model.Request, model.TokenCounter, *HistorySummary) (HistoryResult, error) {
		return HistoryResult{Messages: canonical, Summary: result.Summary}, nil
	}
	_, _, err = prepareRequestHistory(t.Context(), &model.Request{Messages: canonical}, nil, invalidPolicy, canonical, nil)
	require.ErrorContains(t, err, "do not match")
	projected := []*model.Message{systemMsg(), userMsg("different source"), canonical[2], canonical[3]}
	policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
		return requestHistoryResult(req.Messages, "request-only summary", prior)
	}
	ctx, messages, err := prepareRequestHistory(t.Context(), &model.Request{Messages: projected}, nil, policy, canonical, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, messages)
	assert.Nil(t, ctx.Value(preparedHistoryKey{}).(*api.HistoryContext))

	// A System insertion splits two adjacent assistant messages in the actual
	// request. The same non-System prefix is not a full canonical turn.
	canonical = []*model.Message{systemMsg(), userMsg("old"), assistantTextMsg("part one"), assistantTextMsg("part two"), userMsg("current")}
	projected = []*model.Message{canonical[0], canonical[1], canonical[2], historySystemMessage("instruction"), canonical[3], canonical[4]}
	ctx, _, err = prepareRequestHistory(t.Context(), &model.Request{Messages: projected}, nil, policy, canonical, nil)
	require.NoError(t, err)
	assert.Nil(t, ctx.Value(preparedHistoryKey{}).(*api.HistoryContext))
}

func TestRequestHistoryActivityEncodingPreservesOldRecords(t *testing.T) {
	var old PlanActivityInput
	require.NoError(t, json.Unmarshal([]byte(`{"AgentID":"service.agent","RunID":"old"}`), &old))
	assert.Nil(t, old.HistoryContext)
	encoded, err := json.Marshal(old)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "HistoryContext")
	var oldOutput PlanActivityOutput
	require.NoError(t, json.Unmarshal([]byte(`{"PublicationBatchID":"old"}`), &oldOutput))
	assert.Nil(t, oldOutput.HistoryContext)
	encoded, err = json.Marshal(oldOutput)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "HistoryContext")
}

func TestRequestHistoryRequestOnlyLeadingSystemsRemainReusable(t *testing.T) {
	canonical := requestHistoryMessages()
	actual := append([]*model.Message{historySystemMessage("request instruction")}, canonical...)
	calls := 0
	policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
		assert.Equal(t, calls > 0, prior != nil)
		calls++
		return requestHistoryResult(req.Messages, "indexed evidence", prior)
	}
	ctx, _, err := prepareRequestHistory(t.Context(), &model.Request{Messages: actual}, nil, policy, canonical, nil)
	require.NoError(t, err)
	saved := ctx.Value(preparedHistoryKey{}).(*api.HistoryContext)
	require.NotNil(t, saved)
	require.NoError(t, validateHistoryContext(canonical, saved))
	_, _, err = prepareRequestHistory(t.Context(), &model.Request{Messages: actual}, nil, policy, canonical, saved)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

func TestRequestHistoryRichSourceHashesSurviveActivityJSON(t *testing.T) {
	messages, exchanges := completeExchangeHistory(2)
	document := model.DocumentPart{Name: "same document", Format: "txt", Text: "document body", Context: "source context", Cite: true}
	messages[1].Parts = append(messages[1].Parts, document)
	cited := model.CitationsPart{Text: "A cited sentence.", Citations: []model.Citation{{
		Title: "same document", Source: "source", SourceContent: []string{"document body"},
		Location: model.CitationLocation{DocumentChar: &model.DocumentCharLocation{DocumentIndex: 0, Start: 0, End: 8}},
	}}}
	exchanges[0][1].Parts = append(exchanges[0][1].Parts, cited)
	sourceCount := historyConversationCount(parseTurns(messages[1:])[:1])
	original := canonicalHistory(t, messages)
	fresh, reused := 0, 0
	policy := func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
		if prior == nil {
			fresh++
			prior = &HistorySummary{SourceMessages: sourceCount, ReplacedMessages: sourceCount,
				Message: *historySystemMessage("Saved document and completed readings"), PolicyFingerprint: "rich-source-test"}
		} else {
			reused++
		}
		prepared, err := historySummaryMessages(req.Messages, prior)
		return HistoryResult{Messages: prepared, Summary: prior}, err
	}
	pl := &stubPlanner{
		start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			return requestHistoryAnswer(ctx, in.Agent, in.Messages)
		},
		resume: func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return requestHistoryAnswer(ctx, in.Agent, in.Messages)
		},
	}
	rt := newRequestHistoryRuntime(pl, policy)
	input := &PlanActivityInput{AgentID: "service.agent", RunID: "rich-history-run", Messages: messages}
	first, err := rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, first.HistoryContext)
	input.HistoryContext = first.HistoryContext
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	var restored PlanActivityInput
	require.NoError(t, json.Unmarshal(encoded, &restored))
	assert.Equal(t, original, canonicalHistory(t, restored.Messages))
	assert.Equal(t, document, restored.Messages[1].Parts[1])
	assert.Equal(t, exchanges[0][0].Parts[0], restored.Messages[2].Parts[0], "reasoning signature survives")
	assert.Equal(t, cited, restored.Messages[3].Parts[1], "citation coordinates and source text survive")
	assert.Equal(t, exchanges[0][2].Parts[0], restored.Messages[4].Parts[0], "raw tool input and thought signature survive")
	assert.Contains(t, string(encoded), "9007199254740993", "tool result integers remain exact")
	source, positions, complete, err := historySourceHashes(restored.Messages, sourceCount)
	require.NoError(t, err)
	require.True(t, complete)
	assert.Equal(t, first.HistoryContext.SourceSHA256, source)
	assert.Equal(t, first.HistoryContext.SourcePositionsSHA256, positions)
	second, err := rt.PlanResumeActivity(t.Context(), &restored)
	require.NoError(t, err)
	require.NotNil(t, second.HistoryContext, "the restored source is eligible for promotion again")
	assert.Equal(t, first.HistoryContext, second.HistoryContext)
	assert.Equal(t, 1, fresh)
	assert.Equal(t, 1, reused)
	assert.Equal(t, original, canonicalHistory(t, messages))
	assert.Equal(t, original, canonicalHistory(t, restored.Messages))
}

func requestHistoryMessages() []*model.Message {
	return []*model.Message{systemMsg(), userMsg("old request"), assistantTextMsg("old answer"), userMsg("current request")}
}

func requestHistoryResult(messages []*model.Message, text string, prior *HistorySummary) (HistoryResult, error) {
	if prior == nil {
		prior = &HistorySummary{SourceMessages: 2, ReplacedMessages: 2, Message: *historySystemMessage(text), PolicyFingerprint: "test-policy"}
	}
	prepared, err := historySummaryMessages(messages, prior)
	return HistoryResult{Messages: prepared, Summary: prior}, err
}

func newRequestHistoryRuntime(pl *stubPlanner, history HistoryPolicy) *Runtime {
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{complete: func(context.Context, *model.Request) (*model.Response, error) {
		return testModelResponse([]model.Message{*assistantTextMsg("answer")}), nil
	}})
	reg := rt.agents["service.agent"]
	reg.Policy.History = history
	rt.agents["service.agent"] = reg
	return rt
}

func requestHistoryAnswer(ctx context.Context, agent planner.PlannerContext, messages []*model.Message) (*planner.PlanResult, error) {
	client, ok := agent.PlannerModelClient("test")
	if !ok {
		return nil, errors.New("test model missing")
	}
	response, err := client.Complete(ctx, &model.Request{Model: "test", Messages: messages})
	if err != nil {
		return nil, err
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
}
