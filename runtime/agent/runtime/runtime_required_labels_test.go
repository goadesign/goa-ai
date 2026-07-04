//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// TestValidateRequiredLabels is a table-driven unit test for the pure
// boundary check: reg.RequiredLabels is codegen-computed static data (the
// union of every used toolset's RequiredLabels), while labels is the
// genuinely dynamic caller input being validated against it.
func TestValidateRequiredLabels(t *testing.T) {
	cases := []struct {
		name      string
		reg       AgentRegistration
		labels    map[string]string
		wantErr   bool
		wantInMsg []string
	}{
		{
			name: "no required labels is a no-op regardless of input",
			reg:  AgentRegistration{ID: "svc.agent"},
		},
		{
			name:   "all required labels present passes",
			reg:    AgentRegistration{ID: "svc.agent", RequiredLabels: []string{"household_id"}},
			labels: map[string]string{"household_id": "h1"},
		},
		{
			name:      "missing required label fails naming the key",
			reg:       AgentRegistration{ID: "svc.agent", RequiredLabels: []string{"household_id"}},
			labels:    nil,
			wantErr:   true,
			wantInMsg: []string{"household_id", "svc.agent"},
		},
		{
			name:      "missing subset of multiple required labels names only the missing ones",
			reg:       AgentRegistration{ID: "svc.agent", RequiredLabels: []string{"household_id", "tenant_id"}},
			labels:    map[string]string{"household_id": "h1"},
			wantErr:   true,
			wantInMsg: []string{"tenant_id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRequiredLabels(tc.reg, tc.labels)
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
		Engine:       eng,
		logger:       telemetry.NoopLogger{},
		metrics:      telemetry.NoopMetrics{},
		tracer:       telemetry.NoopTracer{},
		SessionStore: sessioninmem.New(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				ID:             "service.agent",
				RequiredLabels: []string{"household_id"},
				Workflow:       engine.WorkflowDefinition{Name: "service.workflow", TaskQueue: "q"},
			},
		},
	}
	_, err := rt.CreateSession(context.Background(), "s1")
	require.NoError(t, err)

	client := rt.MustClient(agent.Ident("service.agent"))

	// Missing label: run-start must fail before the engine ever sees a
	// StartWorkflow call.
	_, err = client.Start(context.Background(), "s1", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMissingLabels)
	require.ErrorContains(t, err, "household_id")
	require.Empty(t, eng.last.Workflow, "engine must never be asked to start a workflow when required labels are missing")

	// Present label: run-start proceeds normally.
	_, err = client.Start(context.Background(), "s1", nil, WithLabels(map[string]string{"household_id": "h1"}))
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
}

// TestStartOneShotRejectsMissingRequiredLabels proves the same run-start
// enforcement applies to the one-shot entry point (Runtime.RunOneShot /
// AgentClient.StartOneShot), not just the session-based Start path.
func TestStartOneShotRejectsMissingRequiredLabels(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:     eng,
		logger:     telemetry.NoopLogger{},
		metrics:    telemetry.NoopMetrics{},
		tracer:     telemetry.NoopTracer{},
		runHandles: make(map[string]engine.WorkflowHandle),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				ID:             "service.agent",
				RequiredLabels: []string{"household_id"},
				Workflow:       engine.WorkflowDefinition{Name: "service.workflow", TaskQueue: "q"},
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
		Engine:       eng,
		logger:       telemetry.NoopLogger{},
		metrics:      telemetry.NoopMetrics{},
		tracer:       telemetry.NoopTracer{},
		SessionStore: sessioninmem.New(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				ID:       "service.agent",
				Workflow: engine.WorkflowDefinition{Name: "service.workflow", TaskQueue: "q"},
			},
		},
	}
	_, err := rt.CreateSession(context.Background(), "s1")
	require.NoError(t, err)
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err = client.Start(context.Background(), "s1", nil)
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
}
