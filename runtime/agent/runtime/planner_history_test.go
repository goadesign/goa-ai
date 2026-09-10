package runtime

// Planner activities deliver full saved history. These tests prove each actual
// model call measures its own request, while code-only decisions do no counting.

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
)

type (
	// requestHistoryProvider records measurement and delivery separately.
	requestHistoryProvider struct {
		counts   []*model.Request
		requests []*model.Request
		countErr error
	}
)

func TestPlanActivitiesSkipUnusedCompression(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "resume"}[resume], func(t *testing.T) {
			provider := &historyCountingClient{}
			plan := func(agent planner.PlannerContext, messages []*model.Message) *planner.PlanResult {
				_, ok := agent.ModelClient("destination")
				require.True(t, ok)
				assert.Len(t, messages, 4)
				return finalPlannerResult("already complete")
			}
			pl := &stubPlanner{
				start: func(_ context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
					return plan(in.Agent, in.Messages), nil
				},
				resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
					return plan(in.Agent, in.Messages), nil
				},
			}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			rt.models["destination"] = historyTestClient(t, provider)
			reg := rt.agents["service.agent"]
			reg.Policy.History = Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtMaxInputTokens: 1, KeepMaxTurns: 1})
			rt.agents["service.agent"] = reg
			call := rt.PlanStartActivity
			if resume {
				call = rt.PlanResumeActivity
			}
			out, err := call(t.Context(), &PlanActivityInput{AgentID: "service.agent", RunID: "test-run", RunContext: run.Context{RunID: "test-run"}, Messages: []*model.Message{userMsg("one"), assistantTextMsg("first"), userMsg("two"), assistantTextMsg("second")}})
			require.NoError(t, err)
			require.NotNil(t, out.Result.FinalResponse)
			assert.False(t, provider.tokenCounted, "retrieval does not count")
			assert.Nil(t, provider.summarized)
		})
	}
}

func TestPlannerHistoryCountsActualDestinationRequests(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[streaming], func(t *testing.T) {
			summary := &historyCountingClient{countErr: errors.New("summary model must not count")}
			first, second := &requestHistoryProvider{}, &requestHistoryProvider{}
			messages := []*model.Message{systemMsg(), userMsg("Original question")}
			before := canonicalHistory(t, messages)
			pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
				assert.Equal(t, before, canonicalHistory(t, in.Messages))
				for index, id := range []string{"first", "second"} {
					client, ok := in.Agent.ModelClient(id)
					require.True(t, ok)
					req := &model.Request{Model: id + "-model", ModelClass: model.ModelClassDefault, Messages: in.Messages, MaxTokens: 100 + index, Temperature: 0.25, Thinking: &model.ThinkingOptions{Enable: true, BudgetTokens: 20 + index}, Tools: fitTools()}
					if index == 1 {
						req.ModelClass = model.ModelClassHighReasoning
						req.Tools = nil
						req.Cache = &model.CacheOptions{AfterTools: true}
					}
					if streaming {
						st, err := client.Stream(ctx, req)
						require.NoError(t, err)
						_, err = planner.ConsumeStream(ctx, st)
						require.NoError(t, err)
					} else {
						_, err := client.Complete(ctx, req)
						require.NoError(t, err)
					}
					if index == 0 {
						assert.Nil(t, req.Cache, "defaults do not mutate planner request")
					}
				}
				return finalPlannerResult("done"), nil
			}}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			rt.models["first"], rt.models["second"] = historyTestClient(t, first), historyTestClient(t, second)
			reg := rt.agents["service.agent"]
			reg.Policy.Cache = CachePolicy{AfterSystem: true}
			reg.Policy.History = Compress(historyTestClient(t, summary), HistoryCompressionConfig{CompressAtMaxInputTokens: 1000, KeepMaxTurns: 1})
			rt.agents["service.agent"] = reg
			_, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{AgentID: "service.agent", RunID: "test-run", RunContext: run.Context{RunID: "test-run"}, Messages: messages})
			require.NoError(t, err)
			for index, provider := range []*requestHistoryProvider{first, second} {
				require.Len(t, provider.counts, 1)
				require.Len(t, provider.requests, 1)
				counted, sent := provider.counts[0], provider.requests[0]
				assert.Equal(t, sent.Model, counted.Model)
				assert.Equal(t, sent.ModelClass, counted.ModelClass)
				assert.Equal(t, sent.MaxTokens, counted.MaxTokens)
				assert.Equal(t, math.Float32bits(sent.Temperature), math.Float32bits(counted.Temperature))
				assert.Equal(t, sent.Thinking, counted.Thinking)
				require.Len(t, counted.Tools, len(sent.Tools))
				for i, tool := range sent.Tools {
					assert.Equal(t, tool.Name, counted.Tools[i].Name)
					assert.Equal(t, tool.Description, counted.Tools[i].Description)
					assert.Equal(t, tool.Input.Contract(), counted.Tools[i].Input.Contract())
				}
				assert.Equal(t, sent.Cache, counted.Cache)
				assert.Equal(t, before, canonicalHistory(t, counted.Messages))
				assert.Equal(t, &model.CacheOptions{AfterSystem: index == 0, AfterTools: index == 1}, counted.Cache)
			}
			assert.Equal(t, before, canonicalHistory(t, messages))
			assert.False(t, summary.tokenCounted)
			assert.Nil(t, summary.summarized)
		})
	}
}

func TestPlannerHistoryFailurePreventsDestinationCall(t *testing.T) {
	countErr := errors.New("destination counter failed: original details")
	destination := &requestHistoryProvider{countErr: countErr}
	summary := &historyCountingClient{}
	pl := &stubPlanner{start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := in.Agent.ModelClient("destination")
		require.True(t, ok)
		_, err := client.Complete(ctx, &model.Request{Messages: in.Messages})
		return nil, err
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["destination"] = historyTestClient(t, destination)
	reg := rt.agents["service.agent"]
	reg.Policy.History = Compress(historyTestClient(t, summary), HistoryCompressionConfig{CompressAtMaxInputTokens: 1, KeepMaxTurns: 1})
	rt.agents["service.agent"] = reg
	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{AgentID: "service.agent", RunID: "test-run", RunContext: run.Context{RunID: "test-run"}, Messages: []*model.Message{userMsg("question")}})
	require.ErrorIs(t, err, countErr)
	assert.Nil(t, out)
	assert.Len(t, destination.counts, 1)
	assert.Empty(t, destination.requests)
	assert.Nil(t, summary.summarized)
}

func TestPlannerHistoryCancellationStopsRequestPreparation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[streaming], func(t *testing.T) {
			destination := &requestHistoryProvider{}
			started := make(chan struct{})
			policy := func(ctx context.Context, _ *model.Request, _ model.TokenCounter, _ *HistorySummary) (HistoryResult, error) {
				close(started)
				<-ctx.Done()
				return HistoryResult{}, ctx.Err()
			}
			client := newRequestConfiguredClient(historyTestClient(t, destination), CachePolicy{}, policy, "service.agent", nil, nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				req := &model.Request{Messages: []*model.Message{userMsg("question")}}
				if streaming {
					_, err := client.Stream(ctx, req)
					done <- err
				} else {
					_, err := client.Complete(ctx, req)
					done <- err
				}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("history preparation did not start")
			}
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("history preparation did not cancel")
			}
			assert.Empty(t, destination.requests)
		})
	}
}

// A caller may handle a failed request and make a later valid request. History
// preparation must not retain an activity-wide error or reuse a failed result.
func TestPlannerHistoryHandledFailureDoesNotPoisonLaterCall(t *testing.T) {
	countErr := errors.New("first request counter failed")
	destination := &requestHistoryProvider{countErr: countErr}
	summary := &historyCountingClient{}
	policy := Compress(historyTestClient(t, summary), HistoryCompressionConfig{CompressAtMaxInputTokens: 1000, KeepMaxTurns: 1})
	client := newRequestConfiguredClient(historyTestClient(t, destination), CachePolicy{}, policy, "service.agent", nil, nil)
	req := &model.Request{Model: "destination-model", Messages: []*model.Message{userMsg("question")}}
	_, err := client.Complete(t.Context(), req)
	require.ErrorIs(t, err, countErr)
	destination.countErr = nil
	response, err := client.Complete(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Len(t, destination.counts, 2)
	assert.Len(t, destination.requests, 1)
	assert.Nil(t, summary.summarized)
}

func (p *requestHistoryProvider) CountTokens(_ context.Context, req *model.Request) (model.TokenCount, error) {
	p.counts = append(p.counts, req)
	return model.TokenCount{InputTokens: 100, Exact: true, Model: req.Model, ModelClass: req.ModelClass}, p.countErr
}

func (p *requestHistoryProvider) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	p.requests = append(p.requests, req)
	return &model.Response{Content: []model.Message{*assistantTextMsg("done")}, StopReason: "stop"}, nil
}

func (p *requestHistoryProvider) Stream(_ context.Context, req *model.Request) (model.Streamer, error) {
	p.requests = append(p.requests, req)
	return &chunkStreamer{
		chunks:   []model.Chunk{model.TextChunk{Message: *assistantTextMsg("done")}, model.StopChunk{Reason: "stop"}},
		response: &model.Response{Content: []model.Message{*assistantTextMsg("done")}, StopReason: "stop"},
	}, nil
}
