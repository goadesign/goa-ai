package runtime

// run_suspension.go owns durable storage of the private checkpoint before a
// workflow reports successful suspension. The workflow sends canonical JSON to
// the existing record activity; the session store preserves those bytes without
// interpreting runtime state.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
)

const runSuspensionRecordType runlog.Type = "runtime.run_suspension"

// persistRunSuspension durably stores one suspension through a retrying
// activity before its workflow is allowed to complete successfully.
func (r *Runtime) persistRunSuspension(wfCtx engine.WorkflowContext, input *RunInput, suspension *api.RunSuspension) error {
	data, err := json.Marshal(suspension)
	if err != nil {
		return fmt.Errorf("encode run suspension for storage: %w", err)
	}
	return wfCtx.PublishRecord(engine.RecordActivityCall{
		Name: recordActivityName,
		Input: &RecordActivityInput{
			Type:      runSuspensionRecordType,
			RunID:     input.RunID,
			AgentID:   input.AgentID,
			SessionID: input.SessionID,
			Payload:   data,
		},
	})
}

// saveRunSuspension validates the private activity variant and stores its
// bytes under the exact run that produced the checkpoint.
func (r *Runtime) saveRunSuspension(ctx context.Context, input *RecordActivityInput) error {
	if input.RunID == "" || input.AgentID == "" || input.SessionID == "" || len(input.Payload) == 0 {
		return errors.New("runtime: suspension record requires run, agent, session, and payload")
	}
	if input.EventKey != "" || input.TurnID != "" || input.TimestampMS != 0 {
		return errors.New("runtime: suspension record cannot contain run-log event fields")
	}
	var suspension api.RunSuspension
	if err := json.Unmarshal(input.Payload, &suspension); err != nil {
		return fmt.Errorf("decode run suspension storage payload: %w", err)
	}
	checkpoint, err := decodeWorkflowCheckpointState(&suspension)
	if err != nil {
		return err
	}
	if checkpoint.AgentID != string(input.AgentID) || checkpoint.SessionID != input.SessionID || checkpoint.BaseContext.RunID != input.RunID {
		return errors.New("runtime: suspension record identity does not match checkpoint")
	}
	return r.SessionStore.SaveRunSuspension(ctx, input.RunID, session.RunSuspension{
		ID:   suspension.ID,
		Data: append([]byte(nil), input.Payload...),
	})
}

// LoadRunSuspension returns and validates the runtime-owned suspension stored
// for one completed run. It never queries workflow-engine retention.
func (r *Runtime) LoadRunSuspension(ctx context.Context, runID string) (*api.RunSuspension, error) {
	stored, err := r.SessionStore.LoadRunSuspension(ctx, runID)
	if err != nil {
		return nil, err
	}
	var suspension api.RunSuspension
	if err := json.Unmarshal(stored.Data, &suspension); err != nil {
		return nil, fmt.Errorf("decode stored run suspension: %w", err)
	}
	if suspension.ID != stored.ID {
		return nil, errors.New("stored run suspension id does not match payload")
	}
	checkpoint, err := decodeWorkflowCheckpointState(&suspension)
	if err != nil {
		return nil, err
	}
	if checkpoint.BaseContext.RunID != runID {
		return nil, errors.New("stored run suspension belongs to another run")
	}
	return &suspension, nil
}
