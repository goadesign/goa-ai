// Published history retains source descriptors through preparation recovery and
// suspension. Only the resumed model request reads the synthetic owner's bytes.
package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// assertNativeImagePreparedContinuation receives history from the real generated
// tool run. The new runtime has historical decoders but no producing tool.
func assertNativeImagePreparedContinuation(t *testing.T, ctx context.Context, messages []*model.Message, owner *imageFixtureOwner) {
	t.Helper()
	const holder = "continued-holder"
	store := inmem.New()
	_, err := store.CreateSession(ctx, holder, time.Now())
	require.NoError(t, err)
	owner.allowed[holder] = true
	readsBefore := len(owner.reads)
	rt := runtime.New(store, runtime.WithImageSourceResolver(genpictures.NativeImageSources(), owner.resolve))
	provider := &imageFixtureProvider{}
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	require.NoError(t, rt.RegisterModel("fixture", client))
	definition := runtime.NewAgentDefinition(runtime.AgentRoute{
		ID: "history.reader", WorkflowName: "history.reader.workflow", DefaultTaskQueue: "history",
	}, nil, nil, nil, nil, nil, nil)
	require.NoError(t, rt.RegisterAgent(ctx, runtime.AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "history.plan", ResumeActivityName: "history.resume", ExecuteToolActivity: "history.execute",
		Planner: &imageFixturePlanner{
			start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				assert.Equal(t, messages, input.Messages)
				assert.Len(t, owner.reads, readsBefore)
				assert.Empty(t, input.Agent.AdvertisedToolDefinitions())
				return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(
					&planner.AwaitClarification{ID: "question", Question: "Which comparison?"},
				))}, nil
			},
			resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				require.Greater(t, len(input.Messages), len(messages))
				assert.Equal(t, messages, input.Messages[:len(messages)])
				assert.Len(t, owner.reads, readsBefore, "history loading and checkpoint decoding do not read images")
				modelClient, ok := input.Agent.ModelClient("fixture")
				require.True(t, ok)
				response, err := modelClient.Complete(ctx, &model.Request{Model: "fixture", Messages: input.Messages})
				if err != nil {
					return nil, err
				}
				return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
			},
		},
	}))
	agentClient := rt.MustClientFor(definition)
	prepared, err := agentClient.Prepare(ctx, holder, messages,
		runtime.WithRunID("history-first"), runtime.WithTurnID("first-turn"))
	require.NoError(t, err)
	prepared = assertNativeImagePreparationRecovery(t, ctx, agentClient, prepared, holder, "history-first")
	assert.Len(t, owner.reads, readsBefore)
	handle, err := agentClient.StartPrepared(ctx, prepared)
	require.NoError(t, err)
	first, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	assert.Equal(t, "goa-ai.run-suspension.v9", first.Suspension.Version)
	assert.NotContains(t, string(first.Suspension.Checkpoint), `"BaseMessages"`)
	assert.NotContains(t, string(first.Suspension.Checkpoint), `"image_source"`)
	assert.Contains(t, string(first.Suspension.Checkpoint), `"HistoryEndID"`)
	assert.Len(t, owner.reads, readsBefore)
	replayed, err := transcript.BuildMessagesFromRunLog(ctx, store, "history-first")
	require.NoError(t, err)
	assert.Equal(t, messages, replayed[:len(messages)])
	assert.Len(t, owner.reads, readsBefore)

	prepared, err = agentClient.PrepareContinuation(ctx, holder, "history-first", "history-second", "second-turn",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "Compare the colors."}},
		runtime.WorkflowOptions{})
	require.NoError(t, err)
	prepared = assertNativeImagePreparationRecovery(t, ctx, agentClient, prepared, holder, "history-second")
	accepted, found, err := store.FindRunPreparation(ctx, storage.PreparationOperation{
		AgentID: "history.reader", SessionID: holder, RunID: "history-second", CommandID: "history-second",
	})
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, storage.SeedContinuation, accepted.Seed.Declaration.Kind)
	page, err := store.ListRunSeedRecords(ctx, "history-second", accepted.Seed.EndID, "", 1)
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.NotNil(t, page.Records[0].Prefix)
	assert.Equal(t, "history-first", page.Records[0].Prefix.RunID)
	assert.NotEmpty(t, page.Records[0].Prefix.EndID)
	assert.Empty(t, page.Records[0].Messages)
	assert.Empty(t, page.NextCursor, "continuation history is one exact reference")
	assert.Len(t, owner.reads, readsBefore)
	handle, err = agentClient.StartPrepared(ctx, prepared)
	require.NoError(t, err)
	second, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.NotNil(t, second.Final)
	assert.Equal(t, "Compared the selected images.", second.Final.Text())
	assert.Equal(t, []string{"photo-21", "photo-2"}, owner.reads[readsBefore:])
	require.Len(t, provider.inputs, 1)
	assert.Len(t, owner.selected, 2, "continuation cannot reexecute the removed producer")
}

// assertNativeImagePreparationRecovery exercises the public reference codec and
// both recovery methods without submitting the prepared workflow.
func assertNativeImagePreparationRecovery(t *testing.T, ctx context.Context, client runtime.AgentClient, prepared *runtime.PreparedRun, holder, runID string) *runtime.PreparedRun {
	t.Helper()
	encoded, err := prepared.MarshalBinary()
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"goa-ai-prepared-run-v3"`)
	assert.NotContains(t, string(encoded), `"image_source"`)
	assert.NotContains(t, string(encoded), "photo-")
	parsed, err := runtime.ParsePreparedRun(encoded)
	require.NoError(t, err)
	recovered, found, err := client.RecoverPrepared(ctx, holder, runID, runID)
	require.NoError(t, err)
	require.True(t, found)
	recoveredBytes, err := recovered.MarshalBinary()
	require.NoError(t, err)
	assert.Equal(t, encoded, recoveredBytes)
	settled, found, err := client.SettlePrepared(ctx, holder, runID, runID, runID)
	require.NoError(t, err)
	require.True(t, found)
	settledBytes, err := settled.MarshalBinary()
	require.NoError(t, err)
	assert.Equal(t, encoded, settledBytes)
	return parsed
}
