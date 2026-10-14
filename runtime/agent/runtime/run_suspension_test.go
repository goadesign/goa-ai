// Package runtime tests durable suspension reads against the run identity that
// owns the stored checkpoint.
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/engine"
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
				run.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.other"))}
			},
			wantText: "does not match run subject",
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
				RunID: "run-1", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
					RunExecutorBranchAgent("svc.agent"))}, SessionID: "session-1",
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
			RunID: "run-1", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent("svc.agent"))}, SessionID: "session-1",
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

// A saved checkpoint is a storage record. Reading it must not acquire the
// transport's smaller payload limit after generated subject validation is added.
func TestStoredCheckpointAboveWorkflowPayloadLimit(t *testing.T) {
	suspension := suspensionContractFixtureWithContext(t, "svc.lookup", "svc.agent", "run-1", nil,
		map[string]any{"saved": strings.Repeat("x", engine.MaxPayloadBytes+1)})
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("svc.agent")}
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		for index := range checkpoint.Batch.Calls {
			checkpoint.Batch.Calls[index].Subject = subject
		}
		for index := range checkpoint.Batch.Result.ToolCalls {
			checkpoint.Batch.Result.ToolCalls[index].Subject = subject
		}
	})
	require.Greater(t, len(suspension.Checkpoint), engine.MaxPayloadBytes)
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	run := session.RunMeta{Subject: subject, RunID: "run-1", SessionID: "session-1", Status: session.RunStatusSuspended}
	decoded, err := validateStoredRunSuspension(session.RunSuspension{ID: suspension.ID, Data: data}, run)
	require.NoError(t, err)
	require.Equal(t, suspension, decoded)
}

func TestStoredCheckpointRejectsMalformedNestedSubjects(t *testing.T) {
	for _, extra := range []string{`,"unknown":true`, `,"service_tool":{}`} {
		suspension := suspensionContractFixture(t, "svc.lookup")
		rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
			subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("svc.agent")}
			for index := range checkpoint.Batch.Calls {
				checkpoint.Batch.Calls[index].Subject = subject
			}
			for index := range checkpoint.Batch.Result.ToolCalls {
				checkpoint.Batch.Result.ToolCalls[index].Subject = subject
			}
		})
		original := []byte(`"type":"agent","value":"svc.agent"`)
		require.Contains(t, string(suspension.Checkpoint), string(original))
		suspension.Checkpoint = bytes.Replace(suspension.Checkpoint, original, append(append([]byte(nil), original...), []byte(extra)...), 1)
		digest := sha256.Sum256(suspension.Checkpoint)
		suspension.ID = hex.EncodeToString(digest[:16])
		_, err := decodeWorkflowCheckpointState(suspension)
		require.ErrorContains(t, err, "saved run subject")
	}
}
