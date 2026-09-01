//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// TestValidateRequiredLabels is a table-driven unit test for the pure
// boundary check: reg.RequiredLabels is codegen-computed static data (the
// union of every used toolset's RequiredLabels), while labels is the
// genuinely dynamic caller input being validated against it.
func TestValidateRequiredLabels(t *testing.T) {
	cases := []struct {
		name       string
		definition AgentDefinition
		labels     map[string]string
		wantErr    bool
		wantInMsg  []string
	}{
		{
			name:       "no required labels is a no-op regardless of input",
			definition: testAgentDefinition("svc.agent", "workflow", "queue", nil, nil),
		},
		{
			name:       "all required labels present passes",
			definition: testAgentDefinition("svc.agent", "workflow", "queue", nil, []string{"household_id"}),
			labels:     map[string]string{"household_id": "h1"},
		},
		{
			name:       "missing required label fails naming the key",
			definition: testAgentDefinition("svc.agent", "workflow", "queue", nil, []string{"household_id"}),
			labels:     nil,
			wantErr:    true,
			wantInMsg:  []string{"household_id", "svc.agent"},
		},
		{
			name:       "missing subset of multiple required labels names only the missing ones",
			definition: testAgentDefinition("svc.agent", "workflow", "queue", nil, []string{"household_id", "tenant_id"}),
			labels:     map[string]string{"household_id": "h1"},
			wantErr:    true,
			wantInMsg:  []string{"tenant_id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRequiredLabels(tc.definition, tc.labels)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorIs(t, err, ErrMissingLabels)
			for _, want := range tc.wantInMsg {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

// TestStartRunRejectsMissingRequiredLabels proves run-start enforcement: a
// session-based Start fails fast, naming the missing label key, before any
// workflow is scheduled with the engine.
func TestStartRunRejectsMissingRequiredLabels(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				Definition: testAgentDefinition("service.agent", "service.workflow", "q", nil, []string{"household_id"}),
			},
		},
	}
	_, err := createSessionForTest(context.Background(), rt.Store, "s1")
	require.NoError(t, err)

	client := rt.MustClient(agent.Ident("service.agent"))

	// Missing label: run-start must fail before the engine ever sees a
	// StartWorkflow call.
	_, err = client.Start(context.Background(), "s1", nil, WithRunID("run-1"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMissingLabels)
	require.ErrorContains(t, err, "household_id")
	require.Empty(t, eng.last.Workflow, "engine must never be asked to start a workflow when required labels are missing")

	// Present label: run-start proceeds normally.
	_, err = client.Start(context.Background(), "s1", nil, WithRunID("run-2"), WithLabels(map[string]string{"household_id": "h1"}))
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
}

// TestStartPreparedContinuationUsesCheckpointRequiredLabels proves a continuation does
// not require callers to repeat trusted labels that the worker restores from
// the preceding workflow's checkpoint.
func TestStartPreparedContinuationUsesCheckpointRequiredLabels(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		agents: map[agent.Ident]AgentRegistration{
			"svc.agent": {
				Definition: testAgentDefinition("svc.agent", "service.workflow", "q", nil, []string{"household_id"}),
			},
		},
	}
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(rt, spec)
	_, err := createSessionForTest(context.Background(), rt.Store, "session-1")
	require.NoError(t, err)
	suspension := suspensionContractFixtureWithContext(
		t, spec.Name, "svc.agent", "run-1",
		map[string]string{"household_id": "house-42"},
		map[string]any{"request_id": "request-42"},
	)
	now := time.Now().UTC()
	admitRunForTest(t, rt.Store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(context.Background(), rt.Store, "run-1", session.RunSuspension{
		ID: suspension.ID, Data: data,
	}))

	client := rt.MustClient(agent.Ident("svc.agent"))
	workflowOptions := WorkflowOptions{
		Memo:             map[string]any{"owner": "house-42"},
		SearchAttributes: map[string]any{"tenant": "house-42"},
	}
	prepared, err := client.PrepareContinuation(
		context.Background(),
		"session-1",
		"run-1",
		"run-2",
		"turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: "clarification-1", Answer: "Building A",
		}},
		workflowOptions,
	)
	require.NoError(t, err)
	handle, err := client.StartPrepared(context.Background(), prepared)
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
	require.Equal(t, "house-42", decodePreparedMemo[string](t, eng.last.Memo, "owner"))
	require.Equal(t, workflowOptions.SearchAttributes, eng.last.SearchAttributes)
	require.Empty(t, eng.last.Input.Labels)
	require.NotNil(t, handle)
}

// TestStartOneShotRejectsMissingRequiredLabels proves the same run-start
// enforcement applies to the one-shot entry point (Runtime.RunOneShot /
// AgentClient.StartOneShot), not just the session-based Start path.
func TestStartOneShotRejectsMissingRequiredLabels(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				Definition: testAgentDefinition("service.agent", "service.workflow", "q", nil, []string{"household_id"}),
			},
		},
	}
	client := rt.MustClient(agent.Ident("service.agent"))

	_, err := client.StartOneShot(context.Background(), nil, WithRunID("run-1"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMissingLabels)
	require.ErrorContains(t, err, "household_id")
	require.Empty(t, eng.last.Workflow)

	_, err = client.StartOneShot(context.Background(), nil, WithRunID("run-2"), WithLabels(map[string]string{"household_id": "h1"}))
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
}

// TestStartRunSucceedsWithoutRequiredLabels regression-proves agents that
// declare no label-backed Inject() fields (the common case, and every
// pre-existing agent regenerating this branch) are entirely unaffected: an
// empty RequiredLabels is a no-op regardless of supplied labels.
func TestStartRunSucceedsWithoutRequiredLabels(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				Definition: testAgentDefinition("service.agent", "service.workflow", "q", nil, nil),
			},
		},
	}
	_, err := createSessionForTest(context.Background(), rt.Store, "s1")
	require.NoError(t, err)
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err = client.Start(context.Background(), "s1", nil, WithRunID("run-1"))
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
}
