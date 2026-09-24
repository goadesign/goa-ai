package runtime

// These tests measure preparation and execution separately. Initial history
// crosses bounded store writes before submission; the accepted workflow starts
// with one publication position and never writes that history a second time.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type initialHistoryCaptureStore struct {
	storage.Store
	seeds   []storage.SeedAppend
	records []*runlog.Event
}

func (s *initialHistoryCaptureStore) AppendRunSeed(ctx context.Context, command storage.SeedAppend) (string, error) {
	s.seeds = append(s.seeds, command)
	return s.Store.AppendRunSeed(ctx, command)
}

func (s *initialHistoryCaptureStore) AppendRunRecord(ctx context.Context, event *runlog.Event) (storage.AppendResult, error) {
	s.records = append(s.records, event)
	return s.Store.AppendRunRecord(ctx, event)
}

func TestInitialHistoryLargeImportStartsWithReferenceAndNoSecondCopy(t *testing.T) {
	base := newTestStore()
	require.NoError(t, createPreparedRunSession(t.Context(), base))
	store := &initialHistoryCaptureStore{Store: base}
	eng := &stubEngine{}
	rt := New(store, WithEngine(eng))
	definition := testAgentDefinition("svc.agent", "agent.workflow", "queue", nil, nil)
	messages := make([]*model.Message, 209)
	for index := range messages {
		role := model.ConversationRoleUser
		if index%2 != 0 {
			role = model.ConversationRoleAssistant
		}
		messages[index] = &model.Message{Role: role, Parts: []model.Part{
			model.TextPart{Text: strings.Repeat("x", 7000)},
		}}
	}
	want, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)
	require.Greater(t, len(want), engine.MaxPayloadBytes)
	plannerCalls := 0
	rt.agents[definition.route.ID] = AgentRegistration{
		Definition: definition, PlanActivityName: "plan",
		Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			plannerCalls++
			actual, err := transcript.EncodeRunLogDelta(input.Messages)
			require.NoError(t, err)
			require.Equal(t, want, actual)
			return finalPlannerResult("finished"), nil
		}},
	}
	client := rt.MustClientFor(definition)
	prepared, err := client.Prepare(t.Context(), "session-1", messages, WithRunID("imported-run"))
	require.NoError(t, err)
	require.Greater(t, len(store.seeds), 1)
	seedWrites := len(store.seeds)
	require.Empty(t, store.records)
	maxSeedBytes := 0
	for index := range store.seeds {
		payload, err := workflowcodec.NewDataConverter().ToPayload(&api.StorageActivityCommand{SeedAppend: &store.seeds[index]})
		require.NoError(t, err)
		size := len(payload.Data)
		for key, value := range payload.Metadata {
			size += len(key) + len(value)
		}
		require.LessOrEqual(t, size, storage.MaxSeedCommandBytes)
		maxSeedBytes = max(maxSeedBytes, size)
	}
	_, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	wire, err := workflowcodec.NewDataConverter().ToPayload(eng.last.Input)
	require.NoError(t, err)
	require.Less(t, len(wire.Data), 4096)
	wf := &testWorkflowContext{ctx: t.Context(), runtime: rt, hookRuntime: rt}
	output, err := rt.ExecuteWorkflow(wf, eng.last.Input)
	require.NoError(t, err)
	require.Equal(t, "finished", output.Final.Text())
	require.Equal(t, 1, plannerCalls)
	require.Len(t, store.seeds, seedWrites)
	maxPostStartBytes := 0
	for _, record := range store.records {
		require.NotEqual(t, transcript.RunLogMessagesSeeded, record.Type)
		maxPostStartBytes = max(maxPostStartBytes, len(record.Payload))
	}
	require.Less(t, maxPostStartBytes, 4096)
	t.Logf("logical_history_bytes=%d seed_writes=%d max_seed_command_bytes=%d workflow_input_bytes=%d max_post_start_record_bytes=%d",
		len(want), seedWrites, maxSeedBytes, len(wire.Data), maxPostStartBytes)
}

func TestInitialHistoryCommandByteBoundary(t *testing.T) {
	// Measure the real encoded command with one ASCII character. Repeating that
	// character changes only the text bytes, not JSON escaping or field presence.
	message := &model.Message{Role: model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "x"}}}
	literal, err := transcript.EncodeRunLogDelta([]*model.Message{message})
	require.NoError(t, err)
	command := api.StorageActivityCommand{SeedAppend: &storage.SeedAppend{
		RunID: "boundary", AttemptID: "boundary",
		Record: storage.SeedRecord{
			Key: "initial-0", PreviousID: storage.EmptySeedEndID, Messages: literal,
		},
	}}
	payload, err := workflowcodec.NewDataConverter().ToPayload(&command)
	require.NoError(t, err)
	framingBytes := len(payload.Data) - 1
	for key, value := range payload.Metadata {
		framingBytes += len(key) + len(value)
	}
	for _, size := range []int{storage.MaxSeedCommandBytes - 1, storage.MaxSeedCommandBytes, storage.MaxSeedCommandBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store := &initialHistoryCaptureStore{Store: newTestStore()}
			messages := []*model.Message{{Role: model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: strings.Repeat("x", size-framingBytes)}}}}
			end, err := publishLiteralHistory(t.Context(), store, storage.SeedDeclaration{
				AgentID: "agent", RunID: "boundary", Kind: storage.SeedLiteral,
			}, messages)
			if size > storage.MaxSeedCommandBytes {
				require.NoError(t, err)
				require.Len(t, store.seeds, 3, "two literal parts followed by compiled start")
				var decoder transcript.LiteralDecoder
				for index, write := range store.seeds[:2] {
					require.NotNil(t, write.Record.LiteralPart)
					require.Equal(t, index == 1, write.Record.LiteralPart.Final)
					got, err := decoder.Append(t.Context(), *write.Record.LiteralPart)
					require.NoError(t, err)
					if index == 1 {
						require.Equal(t, messages, got)
					}
				}
				require.NoError(t, decoder.Finish())
			} else {
				require.NoError(t, err)
				require.Len(t, store.seeds, 2, "history is followed by one compiled-start part")
				actual, err := workflowcodec.NewDataConverter().ToPayload(&api.StorageActivityCommand{SeedAppend: &store.seeds[0]})
				require.NoError(t, err)
				actualSize := len(actual.Data)
				for key, value := range actual.Metadata {
					actualSize += len(key) + len(value)
				}
				require.Equal(t, size, actualSize)
				_, err = store.LoadRunSeed(t.Context(), "boundary", end)
				require.NoError(t, err)
				page, err := store.ListRunSeedRecords(t.Context(), "boundary", end, "", 1)
				require.NoError(t, err)
				require.Len(t, page.Records, 1)
				require.Empty(t, page.NextCursor)
			}
			_, err = store.LoadRun(t.Context(), "boundary")
			require.ErrorIs(t, err, session.ErrRunNotFound)
		})
	}
}

func TestNextTurnValidatesCompletedSourceBeforeFreshInput(t *testing.T) {
	for _, test := range []struct {
		name  string
		prior []*model.Message
		fresh []*model.Message
		want  string
	}{
		{
			name: "empty selected history",
			fresh: []*model.Message{{Role: model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "new question"}}}},
			want: "no non-system messages",
		},
		{
			name: "new result cannot repair incomplete saved tool exchange",
			prior: []*model.Message{{Role: model.ConversationRoleAssistant,
				Parts: []model.Part{model.ToolUsePart{ID: "old-call", Name: "lookup", Input: rawjson.Message(`{}`)}}}},
			fresh: []*model.Message{{Role: model.ConversationRoleUser,
				Parts: []model.Part{model.ToolResultPart{ToolUseID: "old-call", Content: "answer"}}}},
			want: "must be followed by user tool_result",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore()
			rt := New(store, WithEngine(&stubEngine{}))
			input := seedTestPlanInput(t, rt, PlanActivityInput{
				AgentID: "agent", RunID: "source",
				RunContext: run.Context{RunID: "source", SessionID: "session"},
			}, test.prior)
			record, err := prepareHookRecordInput(t.Context(), hooks.NewRunCompletedEvent(
				"source", "agent", "session", "success", run.PhaseCompleted, nil, nil, nil,
			), "")
			require.NoError(t, err)
			_, err = rt.executeStorageCommand(t.Context(), &api.StorageActivityCommand{
				Terminal: &api.RunTerminalCommand{Record: record},
			})
			require.NoError(t, err)
			require.NotEmpty(t, input.HistoryEndID)
			client := rt.MustClientFor(testAgentDefinition("agent", "workflow", "queue", nil, nil))
			_, err = client.PrepareNextTurn(t.Context(), "session", "source", nil, test.fresh, WithRunID("next"))
			require.ErrorContains(t, err, test.want)
			_, err = store.LoadRun(t.Context(), "next")
			require.ErrorIs(t, err, session.ErrRunNotFound)
			_, err = store.LoadRunSeed(t.Context(), "next", storage.EmptySeedEndID)
			require.ErrorIs(t, err, storage.ErrSeedNotFound)
		})
	}
}

func TestLargeInitialHistorySuspendsAndContinuesByReference(t *testing.T) {
	store := &initialHistoryCaptureStore{Store: newTestStore()}
	rt := New(store, WithLogger(telemetry.NoopLogger{}))
	definition := testAgentDefinition("history.agent", "history.workflow", "history.queue", nil, nil)
	messages := make([]*model.Message, 209)
	for index := range messages {
		role := model.ConversationRoleUser
		if index%2 != 0 {
			role = model.ConversationRoleAssistant
		}
		messages[index] = &model.Message{Role: role, Parts: []model.Part{
			model.TextPart{Text: strings.Repeat("saved history ", 550)},
		}}
	}
	want, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)
	require.Greater(t, len(want), engine.MaxPayloadBytes)
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "history.plan", ResumeActivityName: "history.resume", ExecuteToolActivity: "history.tool",
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(
					&planner.AwaitClarification{ID: "question", Question: "Which group?"},
				))}, nil
			},
			resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				require.Greater(t, len(input.Messages), len(messages))
				actual, err := transcript.EncodeRunLogDelta(input.Messages[:len(messages)])
				require.NoError(t, err)
				require.Equal(t, want, actual)
				return finalPlannerResult("continued"), nil
			},
		},
	}))
	_, err = createSessionForTest(t.Context(), store.Store, "history-session")
	require.NoError(t, err)
	client := rt.MustClientFor(definition)
	handle, err := client.Start(t.Context(), "history-session", messages, WithRunID("history-first"), WithTurnID("turn"))
	require.NoError(t, err)
	first, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	require.Less(t, len(first.Suspension.Checkpoint), 4096)
	require.NotContains(t, string(first.Suspension.Checkpoint), `"BaseMessages"`)
	before := len(store.seeds)
	prepared, err := client.PrepareContinuation(t.Context(), "history-session", "history-first", "history-second", "turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "group A"}},
		WorkflowOptions{})
	require.NoError(t, err)
	encoded, err := prepared.MarshalBinary()
	require.NoError(t, err)
	require.Less(t, len(encoded), 8192)
	newSeedWrites := store.seeds[before:]
	var historyRecords, preparedParts int
	for _, write := range newSeedWrites {
		if len(write.Record.Prepared) > 0 {
			preparedParts++
		} else {
			historyRecords++
		}
	}
	require.Equal(t, 1, historyRecords, "continuation stores one history reference")
	require.Positive(t, preparedParts, "continuation stores its compiled start after history")
	require.NotNil(t, store.seeds[before].Record.Prefix)
	require.Empty(t, store.seeds[before].Record.Messages)
	writesBeforeStart := len(store.seeds)
	handle, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	second, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, "continued", second.Final.Text())
	require.Len(t, store.seeds, writesBeforeStart, "continuation start must not copy the predecessor")
	t.Logf("logical_history_bytes=%d checkpoint_bytes=%d continuation_prepared_bytes=%d continuation_history_records=%d continuation_compiled_parts=%d",
		len(want), len(first.Suspension.Checkpoint), len(encoded), historyRecords, preparedParts)
}
