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
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
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
			sessions := sessioninmem.New()
			runtime := &Runtime{
				Engine:        eng,
				logger:        telemetry.NoopLogger{},
				metrics:       telemetry.NoopMetrics{},
				tracer:        telemetry.NoopTracer{},
				RunEventStore: runloginmem.New(),
				SessionStore:  sessions,
				agents: map[agent.Ident]AgentRegistration{
					"svc.agent": {ID: "svc.agent", Workflow: engine.WorkflowDefinition{Name: "agent.workflow", TaskQueue: "q"}},
					"svc.other": {ID: "svc.other", Workflow: engine.WorkflowDefinition{Name: "other.workflow", TaskQueue: "q"}},
				},
			}
			spec := newAnyJSONSpec("svc.lookup")
			seedTestToolSpecs(runtime, spec)
			_, err := runtime.CreateSession(context.Background(), "session-1")
			require.NoError(t, err)
			_, err = runtime.CreateSession(context.Background(), "session-2")
			require.NoError(t, err)
			suspension := suspensionContractFixtureWithContext(
				t, spec.Name, "svc.agent", "run-1", nil, nil,
			)
			now := time.Now().UTC()
			require.NoError(t, sessions.UpsertRun(context.Background(), session.RunMeta{
				AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
				Status: session.RunStatusSuspended, StartedAt: now, UpdatedAt: now,
			}))
			data, err := json.Marshal(suspension)
			require.NoError(t, err)
			require.NoError(t, sessions.SaveRunSuspension(context.Background(), "run-1", session.RunSuspension{
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
