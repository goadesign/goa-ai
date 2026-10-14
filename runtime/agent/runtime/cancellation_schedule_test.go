package runtime

// These workflow tests cancel a tool after its schedule is stored.
// They inspect real runtime writes so a repeated schedule cannot hide behind
// the store's rejection of a changed event with the same key.

import (
	"context"
	"errors"
	"testing"
	"time"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	cancellationScheduleStore struct {
		*testStore
		schedules []*runlog.Event
	}
)

func TestWorkflowCancellationPreservesSchedules(t *testing.T) {
	for _, name := range []string{"success", "returned_cancellation", "context_cancellation", "inline_cancellation", "mixed_error_cancellation"} {
		cancelTool := name != "success"
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			store := &cancellationScheduleStore{testStore: newTestStore()}
			tool := newAnyJSONSpec("synthetic.tools.read")
			firstCause := errors.New("synthetic executor stopped")
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			pl := &stubPlanner{
				start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tool.Name, Payload: rawjson.Message(`{}`)}}}, nil
				},
				resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
					return finalPlannerResult("complete"), nil
				},
			}
			rt := newTestRuntimeWithPlanner("synthetic.agent", pl)
			rt.Store = store
			reg := rt.agents["synthetic.agent"]
			reg.Definition = testRegistrationDefinition("synthetic.agent", engine.WorkflowDefinition{}, []tools.ToolSpec{tool})
			reg.PlanActivityName = "plan"
			reg.ResumeActivityName = "resume"
			reg.ExecuteToolActivity = "execute"
			rt.agents["synthetic.agent"] = reg
			rt.toolsets["synthetic.tools"] = ToolsetRegistration{
				DecodeInExecutor: true,
				Inline:           name == "inline_cancellation",
				Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					require.Len(t, store.schedules, 1, "tool starts only after schedule append")
					page, err := store.ListRunRecords(t.Context(), "run-1", "", 100)
					require.NoError(t, err)
					require.Equal(t, 1, countRunEventsByType(page, hooks.ToolCallScheduled))
					now = now.Add(time.Millisecond)
					if cancelTool {
						if name == "context_cancellation" {
							cancel(firstCause)
						}
						if name == "mixed_error_cancellation" {
							return nil, errors.Join(firstCause, context.Canceled)
						}
						return nil, context.Canceled
					}
					return successfulToolResult(call), nil
				}),
			}
			seedTestToolset(rt, "synthetic.tools", tool)
			seedTestToolDefinitions(rt, tool)
			rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"synthetic.agent": {tool}}
			wf := &testWorkflowContext{ctx: ctx, runtime: rt, hookRuntime: rt, now: func() time.Time { return now }}
			input := &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("synthetic.agent"))}, RunID: "run-1", SessionID: "session-1"}
			publishTestRunInput(t, rt, input, nil)
			output, workflowErr := rt.ExecuteWorkflow(wf, input)
			t.Logf("workflow error: %v; schedule attempts: %d", workflowErr, len(store.schedules))
			if cancelTool {
				assert.Nil(t, output)
				require.ErrorIs(t, workflowErr, context.Canceled)
				switch name {
				case "mixed_error_cancellation":
					require.ErrorIs(t, workflowErr, firstCause)
				case "context_cancellation":
					require.ErrorIs(t, context.Cause(ctx), firstCause, "caller retains the initiating cause even when cancellation stops the wait")
				}
				var conflict *runlog.EventConflictError
				require.NotErrorAs(t, workflowErr, &conflict, "cancellation must not introduce a second schedule")
			} else {
				require.NoError(t, workflowErr)
				assert.NotNil(t, output)
			}
			assert.Len(t, store.schedules, 1, "a stored schedule must not be published again")
			page, err := store.ListRunRecords(t.Context(), "run-1", "", 100)
			require.NoError(t, err)
			assert.Equal(t, 1, countRunEventsByType(page, hooks.ToolCallScheduled))
			expectedResults := 0
			if name == "success" || name == "mixed_error_cancellation" {
				expectedResults = 1
			}
			assert.Equal(t, expectedResults, countRunEventsByType(page, hooks.ToolResultReceived))
			assert.Equal(t, 1, countRunEventsByType(page, hooks.RunCompleted))
			meta, err := store.LoadRun(t.Context(), "run-1")
			require.NoError(t, err)
			switch {
			case name == "mixed_error_cancellation":
				assert.Equal(t, session.RunStatusFailed, meta.Status)
			case cancelTool:
				assert.Equal(t, session.RunStatusCanceled, meta.Status)
			default:
				assert.Equal(t, session.RunStatusCompleted, meta.Status)
				require.Len(t, store.schedules, 1)
				original := store.schedules[0]
				replay, replayErr := store.testStore.AppendRunRecord(t.Context(), original)
				require.NoError(t, replayErr)
				assert.NotEmpty(t, replay.ID)
				changed := *original
				changed.Payload = rawjson.Message(`{"changed":true}`)
				_, collisionErr := store.testStore.AppendRunRecord(t.Context(), &changed)
				var changedConflict *runlog.EventConflictError
				require.ErrorAs(t, collisionErr, &changedConflict, "a genuinely changed event must still be rejected")
				t.Logf("changed-event collision: %v", collisionErr)
			}
			if len(store.schedules) == 2 {
				left, right := store.schedules[0], store.schedules[1]
				assert.Equal(t, left.EventKey, right.EventKey)
				assert.Equal(t, left.Payload, right.Payload)
				assert.NotEqual(t, left.Timestamp, right.Timestamp)
				t.Logf("repeated schedule same key and payload, timestamps: %s -> %s", left.Timestamp, right.Timestamp)
			}
		})
	}
}

// AppendRunRecord retains each schedule attempt before forwarding it to the
// integrated store; callers receive the store's actual result and error.
func (s *cancellationScheduleStore) AppendRunRecord(ctx context.Context, event *runlog.Event) (storage.AppendResult, error) {
	if event.Type == hooks.ToolCallScheduled {
		copied := *event
		s.schedules = append(s.schedules, &copied)
	}
	return s.testStore.AppendRunRecord(ctx, event)
}
