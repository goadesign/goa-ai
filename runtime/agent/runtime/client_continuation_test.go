// client_continuation_test.go verifies continuation identity before a workflow
// or its pending run metadata can be created.
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestStartContinuationRejectsIdentityMismatchBeforeWorkflowStart(t *testing.T) {
	tests := []struct {
		name      string
		agentID   agent.Ident
		sessionID string
		wantError string
	}{
		{name: "agent", agentID: "svc.other", sessionID: "session-1", wantError: "agent mismatch"},
		{name: "session", agentID: "svc.agent", sessionID: "session-2", wantError: "session mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &stubEngine{}
			sessions := newTestStore()
			runtime := &Runtime{
				Engine:  eng,
				logger:  telemetry.NoopLogger{},
				metrics: telemetry.NoopMetrics{},
				tracer:  telemetry.NoopTracer{},
				Store:   sessions,
				agents: map[agent.Ident]AgentRegistration{
					"svc.agent": {ID: "svc.agent", Workflow: engine.WorkflowDefinition{Name: "agent.workflow", TaskQueue: "q"}},
					"svc.other": {ID: "svc.other", Workflow: engine.WorkflowDefinition{Name: "other.workflow", TaskQueue: "q"}},
				},
			}
			spec := newAnyJSONSpec("svc.lookup")
			seedTestToolSpecs(runtime, spec)
			_, err := createSessionForTest(context.Background(), runtime.Store, "session-1")
			require.NoError(t, err)
			_, err = createSessionForTest(context.Background(), runtime.Store, "session-2")
			require.NoError(t, err)
			suspension := suspensionContractFixtureWithContext(
				t, spec.Name, "svc.agent", "run-1", nil, nil,
			)
			now := time.Now().UTC()
			admitRunForTest(t, sessions, session.RunMeta{
				AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
				Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
			})
			data, err := json.Marshal(suspension)
			require.NoError(t, err)
			require.NoError(t, storeSuspensionForTest(context.Background(), sessions, "run-1", session.RunSuspension{
				ID: suspension.ID, Data: data,
			}))

			client := runtime.MustClient(tt.agentID)
			_, err = client.StartContinuation(
				context.Background(),
				tt.sessionID,
				"run-1",
				"run-2",
				"turn-2",
				&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
					ID: "clarification-1", Answer: "Building A",
				}},
				nil,
			)
			require.ErrorContains(t, err, tt.wantError)
			require.Empty(t, eng.last.Workflow)
			_, err = sessions.LoadRun(context.Background(), "run-2")
			require.ErrorIs(t, err, session.ErrRunNotFound)
		})
	}
}

func TestStartContinuationRejectsWrongPendingResponseBeforeWorkflowStart(t *testing.T) {
	eng := &stubEngine{}
	sessions := newTestStore()
	runtime := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   sessions,
		agents: map[agent.Ident]AgentRegistration{
			"svc.agent": {
				ID: "svc.agent",
				Workflow: engine.WorkflowDefinition{
					Name:      "agent.workflow",
					TaskQueue: "q",
				},
			},
		},
	}
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	_, err := createSessionForTest(context.Background(), runtime.Store, "session-1")
	require.NoError(t, err)
	suspension := suspensionContractFixtureWithContext(t, spec.Name, "svc.agent", "run-1", nil, nil)
	now := time.Now().UTC()
	admitRunForTest(t, sessions, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(context.Background(), sessions, "run-1", session.RunSuspension{
		ID: suspension.ID, Data: data,
	}))

	client := runtime.MustClient("svc.agent")
	_, err = client.StartContinuation(
		context.Background(),
		"session-1",
		"run-1",
		"run-2",
		"turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: "clarification-other", Answer: "Building A",
		}},
		nil,
	)
	require.ErrorContains(t, err, "does not match pending id")
	require.Empty(t, eng.last.Workflow)
	_, err = sessions.LoadRun(context.Background(), "run-2")
	require.ErrorIs(t, err, session.ErrRunNotFound)

	directInput, err := buildContinuationRunInput(
		"svc.agent",
		"session-1",
		"run-3",
		"turn-3",
		suspension,
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: "clarification-other",
		}},
	)
	require.NoError(t, err)
	_, err = runtime.startRunOn(context.Background(), directInput, "agent.workflow", "q", true)
	require.ErrorContains(t, err, "does not match pending id")
	require.Empty(t, eng.last.Workflow)
	_, err = sessions.LoadRun(context.Background(), "run-3")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}
