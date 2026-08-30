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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suspension := suspensionContractFixtureWithContext(
				t, "svc.lookup", "svc.agent", "run-1", nil, nil,
			)
			data, err := json.Marshal(suspension)
			require.NoError(t, err)
			run := session.RunMeta{
				RunID: "run-1", AgentID: "svc.agent", SessionID: "session-1",
				Status: session.RunStatusSuspended,
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

func (s suspensionReadStore) LoadRun(context.Context, string) (session.RunMeta, error) {
	return s.run, nil
}

func (s suspensionReadStore) LoadRunSuspension(context.Context, string) (session.RunSuspension, error) {
	return s.suspension, nil
}
