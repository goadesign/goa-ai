package runtime

// These tests use real planner activities and model observers. A summary must
// follow the exact chosen response, never whichever helper call finishes last.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"time"
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
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "history-run", RunContext: run.Context{RunID: "history-run"}}, messages)
	out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *(input), nil))
	require.NoError(t, err)
	require.NotNil(t, out.HistoryContext)
	assert.Equal(t, original, canonicalHistory(t, testActivityMessages(t, rt, input)))
	assert.Equal(t, "answer", out.Transcript[0].Parts[0].(model.TextPart).Text)
	input.HistoryEndID = appendTestActivityHistory(t, rt, input, append(out.Transcript, userMsg("follow-up")))
	input.HistoryContext = out.HistoryContext
	out, err = rt.PlanResumeActivity(t.Context(), seedTestPlanInput(t, rt, *(input), nil))
	require.NoError(t, err)
	require.NotNil(t, out.HistoryContext)
	assert.Equal(t, 1, fresh)
	assert.Equal(t, 1, reused)
	assert.Equal(t, original, canonicalHistory(t, testActivityMessages(t, rt, input)[:len(messages)]))
}

func TestRequestHistoryCompressCarriesContextAndReplacesOldFingerprint(t *testing.T) {
	for _, hasSystem := range []bool{false, true} {
		t.Run(map[bool]string{false: "conversation goals", true: "system goals"}[hasSystem], func(t *testing.T) {
			provider := evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"})
			policy := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1})
			answer := func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
				return requestHistoryAnswer(ctx, in.Agent, in.Messages)
			}
			rt := newRequestHistoryRuntime(&stubPlanner{start: answer, resume: func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
				return requestHistoryAnswer(ctx, in.Agent, in.Messages)
			}}, policy)
			messages := requestHistoryMessages()
			if !hasSystem {
				messages = messages[1:]
			}
			before := canonicalHistory(t, messages)
			input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "history-run"}, messages)
			out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *(input), nil))
			require.NoError(t, err)
			require.NotNil(t, out.HistoryContext)
			input.HistoryContext = out.HistoryContext
			encoded, err := json.Marshal(input)
			require.NoError(t, err)
			var restored PlanActivityInput
			require.NoError(t, json.Unmarshal(encoded, &restored))
			out, err = rt.PlanResumeActivity(t.Context(), seedTestPlanInput(t, rt, restored, nil))
			require.NoError(t, err)
			assert.Equal(t, 1, provider.completeCalls)
			assert.Equal(t, input.HistoryContext, out.HistoryContext)

			// Recorded old summary state remains valid durable data. A new
			// activity regenerates its content once under the current policy.
			restored.HistoryContext.Summary.PolicyFingerprint = "earlier-summary-content-contract"
			require.NoError(t, validateHistoryContext(testActivityMessages(t, rt, &restored), restored.HistoryContext))
			out, err = rt.PlanResumeActivity(t.Context(), seedTestPlanInput(t, rt, restored, nil))
			require.NoError(t, err)
			require.NotNil(t, out.HistoryContext)
			assert.Equal(t, 2, provider.completeCalls)
			restored.HistoryContext = out.HistoryContext
			_, err = rt.PlanResumeActivity(t.Context(), seedTestPlanInput(t, rt, restored, nil))
			require.NoError(t, err)
			assert.Equal(t, 2, provider.completeCalls)
			assert.Equal(t, before, canonicalHistory(t, testActivityMessages(t, rt, &restored)))
		})
	}
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
	out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "history-run"}, requestHistoryMessages()))
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
	out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "history-run"}, requestHistoryMessages()))
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
			out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "history-run"}, requestHistoryMessages()))
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
			out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "history-run"}, requestHistoryMessages()))
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
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "rich-history-run"}, messages)
	first, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *(input), nil))
	require.NoError(t, err)
	require.NotNil(t, first.HistoryContext)
	input.HistoryContext = first.HistoryContext
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	var restored PlanActivityInput
	require.NoError(t, json.Unmarshal(encoded, &restored))
	restoredMessages := testActivityMessages(t, rt, &restored)
	assert.Equal(t, original, canonicalHistory(t, restoredMessages))
	assert.Equal(t, document, restoredMessages[1].Parts[1])
	assert.Equal(t, exchanges[0][0].Parts[0], restoredMessages[2].Parts[0], "reasoning signature survives")
	assert.Equal(t, cited, restoredMessages[3].Parts[1], "citation coordinates and source text survive")
	assert.Equal(t, exchanges[0][2].Parts[0], restoredMessages[4].Parts[0], "raw tool input and thought signature survive")
	assert.Contains(t, strings.Join(canonicalHistory(t, restoredMessages), "\n"), "9007199254740993", "tool result integers remain exact in stored history")
	assert.NotContains(t, string(encoded), "9007199254740993", "the activity command does not repeat saved tool results")
	source, positions, complete, err := historySourceHashes(restoredMessages, sourceCount)
	require.NoError(t, err)
	require.True(t, complete)
	assert.Equal(t, first.HistoryContext.SourceSHA256, source)
	assert.Equal(t, first.HistoryContext.SourcePositionsSHA256, positions)
	second, err := rt.PlanResumeActivity(t.Context(), seedTestPlanInput(t, rt, restored, nil))
	require.NoError(t, err)
	require.NotNil(t, second.HistoryContext, "the restored source is eligible for promotion again")
	assert.Equal(t, first.HistoryContext, second.HistoryContext)
	assert.Equal(t, 1, fresh)
	assert.Equal(t, 1, reused)
	assert.Equal(t, original, canonicalHistory(t, messages))
	assert.Equal(t, original, canonicalHistory(t, testActivityMessages(t, rt, &restored)))
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

func TestRequestHistoryCompressPublishesSummaryAndPlannerUsage(t *testing.T) {
	for _, rawClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "designated planner client", true: "raw model client"}[rawClient], func(t *testing.T) {
			summaryUsage := model.TokenUsage{
				Model:        "provider-summary",
				ModelClass:   model.ModelClassSmall,
				InputTokens:  11,
				OutputTokens: 4,
				TotalTokens:  15,
			}
			plannerUsage := model.TokenUsage{
				Model:        "provider-planner",
				ModelClass:   model.ModelClassDefault,
				InputTokens:  20,
				OutputTokens: 7,
				TotalTokens:  27,
			}
			summaryCalls := 0
			summaryClient := historyTestClient(t, stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					summaryCalls++
					return testModelResponseWithUsage(
						[]model.Message{*assistantTextMsg("Earlier observations")},
						summaryUsage,
					), nil
				},
			})
			policy := Compress(
				summaryClient,
				HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1},
				WithModelClass(model.ModelClassSmall),
			)
			callPlanner := func(ctx context.Context, agent planner.PlannerContext, messages []*model.Message) (*planner.PlanResult, error) {
				req := &model.Request{
					Model:      "planner-model",
					ModelClass: model.ModelClassDefault,
					Messages:   messages,
				}
				if rawClient {
					cli, ok := agent.ModelClient("test")
					require.True(t, ok)
					resp, err := cli.Complete(ctx, req)
					if err != nil {
						return nil, err
					}
					return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &resp.Content[0]}}, nil
				}
				cli, ok := agent.PlannerModelClient("test")
				require.True(t, ok)
				resp, err := cli.Complete(ctx, req)
				if err != nil {
					return nil, err
				}
				return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &resp.Content[0]}}, nil
			}
			pl := &stubPlanner{
				start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
					return callPlanner(ctx, in.Agent, in.Messages)
				},
				resume: func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
					return callPlanner(ctx, in.Agent, in.Messages)
				},
			}
			rt := newRequestHistoryRuntime(pl, policy)
			tracer := &recordingTelemetryTracer{}
			rt.tracer = tracer
			rt.models["test"] = mustTestModelClient(stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					return testModelResponseWithUsage(
						[]model.Message{*assistantTextMsg("answer")},
						plannerUsage,
					), nil
				},
			})

			input := seedTestPlanInput(t, rt, PlanActivityInput{
				AgentID:    "service.agent",
				RunID:      "history-usage-run",
				RunContext: run.Context{RunID: "history-usage-run", SessionID: "sess-1"},
			}, requestHistoryMessages())
			first, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *input, nil))
			require.NoError(t, err)
			require.NotNil(t, first.HistoryContext)
			assert.Equal(t, 1, summaryCalls)
			assert.Equal(t, model.TokenUsage{
				InputTokens:  31,
				OutputTokens: 11,
				TotalTokens:  42,
			}, first.Usage)
			assert.Equal(t, []model.TokenUsage{summaryUsage, plannerUsage}, plannerOutputUsageEvents(t, first))
			assert.Equal(t, 2, countFinishedModelCompleteSpans(tracer))

			// Reused summary on the next turn must not re-run or re-bill the summary call.
			input.HistoryContext = first.HistoryContext
			second, err := rt.PlanResumeActivity(t.Context(), seedTestPlanInput(t, rt, *input, nil))
			require.NoError(t, err)
			require.NotNil(t, second.HistoryContext)
			assert.Equal(t, 1, summaryCalls)
			assert.Equal(t, model.TokenUsage{
				InputTokens:  20,
				OutputTokens: 7,
				TotalTokens:  27,
			}, second.Usage)
			assert.Equal(t, []model.TokenUsage{plannerUsage}, plannerOutputUsageEvents(t, second))
		})
	}
}

func TestRequestHistoryCompressCertifiesProviderRecoveryAndRetainsSummaryUsage(t *testing.T) {
	summaryUsage := model.TokenUsage{
		Model:        "provider-summary",
		ModelClass:   model.ModelClassSmall,
		InputTokens:  6,
		OutputTokens: 3,
		TotalTokens:  9,
	}
	plannerUsage := model.TokenUsage{
		Model:        "provider-planner",
		ModelClass:   model.ModelClassDefault,
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
	}
	rateLimitErr := model.NewProviderError(
		"synthetic",
		"complete",
		429,
		model.ProviderErrorKindRateLimited,
		"rate_limited",
		"rate limited",
		"req-1",
		true,
		nil,
	)

	t.Run("summary succeeds then planner fails retryably", func(t *testing.T) {
		summaryCalls := 0
		plannerCalls := 0
		summaryClient := historyTestClient(t, stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				summaryCalls++
				return testModelResponseWithUsage(
					[]model.Message{*assistantTextMsg("Earlier observations")},
					summaryUsage,
				), nil
			},
		})
		policy := Compress(
			summaryClient,
			HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1},
			WithModelClass(model.ModelClassSmall),
		)
		pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			return requestHistoryAnswer(ctx, in.Agent, in.Messages)
		}}
		rt := newRequestHistoryRuntime(pl, policy)
		rt.models["test"] = mustTestModelClient(stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				plannerCalls++
				if plannerCalls == 1 {
					return nil, rateLimitErr
				}
				return testModelResponseWithUsage(
					[]model.Message{*assistantTextMsg("recovered answer")},
					plannerUsage,
				), nil
			},
		})

		input := seedTestPlanInput(t, rt, PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "history-retry-run",
			RunContext: run.Context{RunID: "history-retry-run", SessionID: "sess-1", TurnID: "turn-1"},
			Policy:     &PolicyOverrides{ProviderRetryBudget: time.Hour},
		}, requestHistoryMessages())

		firstAttempt, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *input, nil))
		require.NoError(t, err)
		require.NotNil(t, firstAttempt.ProviderFailure)
		assert.Equal(t, model.TokenUsage{
			InputTokens:  6,
			OutputTokens: 3,
			TotalTokens:  9,
		}, firstAttempt.Usage)
		assert.Equal(t, []model.TokenUsage{summaryUsage}, plannerOutputUsageEvents(t, firstAttempt))

		// Verify runPlanActivity aggregates the first attempt's summary usage with the retry attempt.
		plannerCalls = 0
		summaryCalls = 0
		base := &workflowConversation{
			finalization:     &workflowFinalizationState{},
			providerRecovery: &providerRecoveryBudget{Remaining: time.Hour},
		}
		w := &providerRecoveryWorkflow{clock: time.Unix(100, 0)}
		w.routeWorkflowContext = &routeWorkflowContext{
			ctx:         t.Context(),
			runID:       input.RunID,
			hookRuntime: rt,
			plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
				"plan": func(ctx context.Context, actual *PlanActivityInput) (*PlanActivityOutput, error) {
					w.clock = w.clock.Add(time.Second)
					return rt.PlanStartActivity(ctx, actual)
				},
			},
		}
		budget := w.Now().Add(15 * time.Minute)
		hard := budget.Add(time.Minute)
		wf, err := installProviderRecovery(w, base.providerRecovery)
		require.NoError(t, err)
		base.providerControl = wf.actor
		wf.actor.budget = &budget
		wf.actor.hard = &hard
		recovered, err := rt.runPlanActivity(
			wf,
			"plan",
			engine.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: engine.RetryPolicy{MaxAttempts: 1}},
			*seedTestPlanInput(t, rt, *input, nil),
			base,
			budget,
		)
		require.NoError(t, err)
		require.NotNil(t, recovered.Result)
		assert.Equal(t, 2, summaryCalls)
		assert.Equal(t, 2, plannerCalls)
		assert.Equal(t, model.TokenUsage{
			InputTokens:  22,
			OutputTokens: 11,
			TotalTokens:  33,
		}, recovered.Usage)
	})

	t.Run("summary fails retryably with usage", func(t *testing.T) {
		summaryClient := historyTestClient(t, stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				return nil, model.RetainUsage(rateLimitErr, summaryUsage)
			},
		})
		policy := Compress(
			summaryClient,
			HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1},
			WithModelClass(model.ModelClassSmall),
		)
		pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			return requestHistoryAnswer(ctx, in.Agent, in.Messages)
		}}
		rt := newRequestHistoryRuntime(pl, policy)
		input := seedTestPlanInput(t, rt, PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "history-summary-retry-run",
			RunContext: run.Context{RunID: "history-summary-retry-run"},
			Policy:     &PolicyOverrides{ProviderRetryBudget: time.Hour},
		}, requestHistoryMessages())

		out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *input, nil))
		require.NoError(t, err)
		require.NotNil(t, out.ProviderFailure)
		assert.Equal(t, model.ProviderErrorKindRateLimited, out.ProviderFailure.Kind())
		assert.Equal(t, model.TokenUsage{
			InputTokens:  6,
			OutputTokens: 3,
			TotalTokens:  9,
		}, out.Usage)
		assert.Equal(t, []model.TokenUsage{summaryUsage}, plannerOutputUsageEvents(t, out))
	})
}

func TestRequestHistoryCompressPublishesTerminalFailureAndRejectedPlannerUsage(t *testing.T) {
	summaryUsage := model.TokenUsage{
		Model:        "provider-summary",
		ModelClass:   model.ModelClassSmall,
		InputTokens:  5,
		OutputTokens: 4,
		TotalTokens:  9,
	}
	plannerRejectedUsage := model.TokenUsage{
		Model:        "provider-planner",
		ModelClass:   model.ModelClassDefault,
		InputTokens:  8,
		OutputTokens: 3,
		TotalTokens:  11,
	}
	terminalProviderErr := model.NewProviderError(
		"synthetic",
		"complete",
		400,
		model.ProviderErrorKindInvalidRequest,
		"invalid_request",
		"invalid request",
		"req-terminal",
		false,
		nil,
	)

	t.Run("failed summary terminal with and without usage keeps identical failure shape", func(t *testing.T) {
		for _, reportUsage := range []bool{true, false} {
			summaryClient := historyTestClient(t, stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					if reportUsage {
						return nil, model.RetainUsage(terminalProviderErr, summaryUsage)
					}
					return nil, model.RetainUsage(terminalProviderErr, model.TokenUsage{
						Model:      "provider-summary",
						ModelClass: model.ModelClassSmall,
					})
				},
			})
			policy := Compress(
				summaryClient,
				HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1},
				WithModelClass(model.ModelClassSmall),
			)
			pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
				return requestHistoryAnswer(ctx, in.Agent, in.Messages)
			}}
			rt := newRequestHistoryRuntime(pl, policy)
			input := seedTestPlanInput(t, rt, PlanActivityInput{
				AgentID:    "service.agent",
				RunID:      "history-summary-terminal-run",
				RunContext: run.Context{RunID: "history-summary-terminal-run"},
			}, requestHistoryMessages())

			out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *input, nil))
			require.NoError(t, err)
			require.NotNil(t, out)
			require.NotNil(t, out.PlanningFailure)
			assert.Equal(t, string(model.ProviderErrorKindInvalidRequest), out.PlanningFailure.Kind)
			if reportUsage {
				assert.Equal(t, model.TokenUsage{
					InputTokens:  5,
					OutputTokens: 4,
					TotalTokens:  9,
				}, out.Usage)
				assert.Equal(t, []model.TokenUsage{summaryUsage}, plannerOutputUsageEvents(t, out))
			} else {
				assert.Equal(t, model.TokenUsage{}, out.Usage)
				assert.Empty(t, plannerOutputUsageEvents(t, out))
			}
		}
	})

	t.Run("summary followed by rejected planner terminal usage", func(t *testing.T) {
		summaryClient := historyTestClient(t, stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				return testModelResponseWithUsage(
					[]model.Message{*assistantTextMsg("Earlier observations")},
					summaryUsage,
				), nil
			},
		})
		policy := Compress(
			summaryClient,
			HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1},
			WithModelClass(model.ModelClassSmall),
		)
		pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			client, ok := in.Agent.PlannerModelClient("test")
			require.True(t, ok)
			resp, err := client.Complete(ctx, &model.Request{
				Model:      "planner-model",
				ModelClass: model.ModelClassDefault,
				Messages:   in.Messages,
				Tools:      fitTools(),
			})
			if err != nil {
				return nil, err
			}
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &resp.Content[0]}}, nil
		}}
		rt := newRequestHistoryRuntime(pl, policy)
		rt.models["test"] = mustTestModelClient(stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				return &model.Response{
					Content: []model.Message{{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{nil},
					}},
					StopReason: "end_turn",
					Usage:      plannerRejectedUsage,
				}, nil
			},
		})
		input := seedTestPlanInput(t, rt, PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "history-planner-rejected-run",
			RunContext: run.Context{RunID: "history-planner-rejected-run"},
		}, requestHistoryMessages())

		out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *input, nil))
		require.NoError(t, err)
		require.NotNil(t, out.OutputContractFailure)
		assert.Equal(t, planner.OutputContractOriginModel, out.OutputContractFailure.Origin)
		assert.Equal(t, model.TokenUsage{
			InputTokens:  13,
			OutputTokens: 7,
			TotalTokens:  20,
		}, out.Usage)
		assert.Equal(t, []model.TokenUsage{summaryUsage, plannerRejectedUsage}, plannerOutputUsageEvents(t, out))
	})

	t.Run("non-summary planner failure returns activity error while summary planner failure preserves terminal failure and failUnproven", func(t *testing.T) {
		nonSummaryPlanner := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			client, ok := in.Agent.PlannerModelClient("test")
			require.True(t, ok)
			_, err := client.Complete(ctx, &model.Request{Model: "planner-model", Messages: in.Messages})
			require.NoError(t, err)
			return nil, terminalProviderErr
		}}
		nonSummaryRT := newTestRuntimeWithPlanner("service.agent", nonSummaryPlanner)
		nonSummaryRT.models["test"] = mustTestModelClient(stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				return testModelResponse([]model.Message{*assistantTextMsg("answer")}), nil
			},
		})
		nonSummaryInput := seedTestPlanInput(t, nonSummaryRT, PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "non-summary-failure-run",
			RunContext: run.Context{RunID: "non-summary-failure-run"},
		}, requestHistoryMessages())
		nonSummaryOut, nonSummaryErr := nonSummaryRT.PlanStartActivity(t.Context(), seedTestPlanInput(t, nonSummaryRT, *nonSummaryInput, nil))
		require.Nil(t, nonSummaryOut)
		require.ErrorIs(t, nonSummaryErr, terminalProviderErr)

		// With a summary call, runPlanActivity returns the same terminal failure facts
		// and closes active provider recovery via failUnproven while publishing summary usage.
		rateLimitErr := model.NewProviderError(
			"synthetic",
			"complete",
			429,
			model.ProviderErrorKindRateLimited,
			"rate_limited",
			"rate limited",
			"req-1",
			true,
			nil,
		)
		summaryClient := historyTestClient(t, stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				return testModelResponseWithUsage(
					[]model.Message{*assistantTextMsg("Earlier observations")},
					summaryUsage,
				), nil
			},
		})
		policy := Compress(
			summaryClient,
			HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1},
			WithModelClass(model.ModelClassSmall),
		)
		attempt := 0
		pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			attempt++
			client, ok := in.Agent.PlannerModelClient("test")
			require.True(t, ok)
			_, err := client.Complete(ctx, &model.Request{Model: "planner-model", Messages: in.Messages})
			if err != nil {
				return nil, err
			}
			return nil, terminalProviderErr
		}}
		rt := newRequestHistoryRuntime(pl, policy)
		rt.models["test"] = mustTestModelClient(stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				if attempt == 1 {
					return nil, rateLimitErr
				}
				return testModelResponse([]model.Message{*assistantTextMsg("answer")}), nil
			},
		})
		input := seedTestPlanInput(t, rt, PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "summary-fail-unproven-run",
			RunContext: run.Context{RunID: "summary-fail-unproven-run", SessionID: "sess-1", TurnID: "turn-1"},
			Policy:     &PolicyOverrides{ProviderRetryBudget: time.Hour},
		}, requestHistoryMessages())
		base := &workflowConversation{
			finalization:     &workflowFinalizationState{},
			providerRecovery: &providerRecoveryBudget{Remaining: time.Hour},
		}
		w := &providerRecoveryWorkflow{clock: time.Unix(100, 0)}
		w.routeWorkflowContext = &routeWorkflowContext{
			ctx:         t.Context(),
			runID:       input.RunID,
			hookRuntime: rt,
			plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
				"plan": func(ctx context.Context, actual *PlanActivityInput) (*PlanActivityOutput, error) {
					w.clock = w.clock.Add(time.Second)
					return rt.PlanStartActivity(ctx, actual)
				},
			},
		}
		budget := w.Now().Add(15 * time.Minute)
		hard := budget.Add(time.Minute)
		wf, err := installProviderRecovery(w, base.providerRecovery)
		require.NoError(t, err)
		base.providerControl = wf.actor
		wf.actor.budget = &budget
		wf.actor.hard = &hard
		failedOut, failedErr := rt.runPlanActivity(
			wf,
			"plan",
			engine.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: engine.RetryPolicy{MaxAttempts: 1}},
			*seedTestPlanInput(t, rt, *input, nil),
			base,
			budget,
		)
		require.Error(t, failedErr)
		require.NotNil(t, failedOut)
		assert.Equal(t, hooks.RunFailureFromError(nonSummaryErr).Kind, hooks.RunFailureFromError(failedErr).Kind)
		assert.Equal(t, hooks.RunFailureFromError(nonSummaryErr).Retryable, hooks.RunFailureFromError(failedErr).Retryable)
		assert.Equal(t, model.TokenUsage{
			InputTokens:  10,
			OutputTokens: 8,
			TotalTokens:  18,
		}, failedOut.Usage)
	})
}

func TestCompressStandaloneLeavesSuppliedClientUnmodified(t *testing.T) {
	calls := 0
	client := historyTestClient(t, stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			calls++
			return testModelResponseWithUsage(
				[]model.Message{*assistantTextMsg("standalone summary")},
				model.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6},
			), nil
		},
	})
	policy := Compress(client, HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1})
	res, err := policy(t.Context(), &model.Request{Messages: requestHistoryMessages()}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, res.Summary)
	assert.Equal(t, 1, calls)
	assert.Contains(t, res.Summary.Message.Parts[0].(model.TextPart).Text, "standalone summary")
}

func plannerOutputUsageEvents(t *testing.T, out *PlanActivityOutput) []model.TokenUsage {
	t.Helper()
	var usage []model.TokenUsage
	for _, record := range out.PlannerEvents {
		if record.Type != hooks.Usage {
			continue
		}
		var event hooks.UsageEvent
		require.NoError(t, json.Unmarshal(record.Payload, &event))
		usage = append(usage, event.TokenUsage)
	}
	return usage
}

func countFinishedModelCompleteSpans(tracer *recordingTelemetryTracer) int {
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	count := 0
	for _, span := range tracer.spans {
		if strings.HasPrefix(span.name, "chat ") && span.ended {
			count++
		}
	}
	return count
}
