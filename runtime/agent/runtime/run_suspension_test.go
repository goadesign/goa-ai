// Package runtime tests durable suspension reads against the run identity that
// owns the stored checkpoint.
package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type suspensionReadStore struct {
	storage.Store
	run        session.RunMeta
	suspension session.RunSuspension
}

func TestLoadRunSuspensionRejectsCheckpointOwnerMismatch(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*session.RunMeta)
		wantText string
	}{
		{
			name: "agent",
			mutate: func(run *session.RunMeta) {
				run.AgentID = "svc.other"
			},
			wantText: "does not match run agent",
		},
		{
			name: "session",
			mutate: func(run *session.RunMeta) {
				run.SessionID = "session-other"
			},
			wantText: "does not match run session",
		},
		{
			name: "parent",
			mutate: func(run *session.RunMeta) {
				run.ParentRunID = "parent-other"
			},
			wantText: "does not match run parent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suspension := suspensionContractFixtureWithContext(
				t, "svc.lookup", "svc.agent", "run-1", map[string]string{"site": "one"}, nil,
			)
			rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
				checkpoint.Context.ParentRunID = "parent"
			})
			data, err := json.Marshal(suspension)
			require.NoError(t, err)
			run := session.RunMeta{
				RunID: "run-1", AgentID: "svc.agent", SessionID: "session-1",
				ParentRunID: "parent", Status: session.RunStatusSuspended,
				Labels: map[string]string{"site": "one"},
			}
			test.mutate(&run)
			store := suspensionReadStore{
				run: run,
				suspension: session.RunSuspension{
					ID: suspension.ID, Data: data,
				},
			}

			_, err = New(store).LoadRunSuspension(t.Context(), run.RunID)
			require.ErrorIs(t, err, ErrRunSuspensionCorrupt)
			require.ErrorContains(t, err, test.wantText)
		})
	}
}

func TestLoadRunSuspensionAcceptsLabelsAddedAfterRunStart(t *testing.T) {
	suspension := suspensionContractFixtureWithContext(
		t, "svc.lookup", "svc.agent", "run-1", map[string]string{
			"site":   "one",
			"policy": "approved",
		}, nil,
	)
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	store := suspensionReadStore{
		run: session.RunMeta{
			RunID: "run-1", AgentID: "svc.agent", SessionID: "session-1",
			Status: session.RunStatusSuspended, Labels: map[string]string{"site": "one"},
		},
		suspension: session.RunSuspension{ID: suspension.ID, Data: data},
	}

	stored, err := New(store).LoadRunSuspension(t.Context(), store.run.RunID)
	require.NoError(t, err)
	require.Equal(t, suspension, stored)
}

func (s suspensionReadStore) LoadRun(context.Context, string) (session.RunMeta, error) {
	return s.run, nil
}

func (s suspensionReadStore) LoadRunSuspension(context.Context, string) (session.RunSuspension, error) {
	return s.suspension, nil
}
