package runtime

// run_suspension.go owns durable storage of the private checkpoint before a
// workflow reports successful suspension. The workflow sends canonical JSON to
// the storage activity; the runtime store preserves those bytes without
// interpreting runtime state.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
)

const (
	runSuspensionRecordType       runlog.Type = "runtime.run_suspension"
	repairedRunSuspensionEventKey string      = "runtime/repaired-suspension"
)

var (
	// ErrRunSuspensionNotReady indicates that a running or paused run has not
	// durably recorded a RunSuspended event.
	ErrRunSuspensionNotReady = errors.New("run suspension not ready")
	// ErrRunSuspensionCorrupt indicates that a run recorded as suspended has no
	// valid checkpoint from which a continuation can start.
	ErrRunSuspensionCorrupt = errors.New("run suspension corrupt")
)

// persistRunSuspension stores the checkpoint, suspended record, and terminal
// state together before the workflow is allowed to complete successfully.
func (r *Runtime) persistRunSuspension(wfCtx engine.WorkflowContext, input *RunInput, suspension *api.RunSuspension) error {
	records, err := prepareRunSuspensionRecords(wfCtx.Context(), input, suspension)
	if err != nil {
		return err
	}
	command := &api.StorageActivityCommand{Suspension: &api.RunSuspensionCommand{
		Checkpoint: records[0],
		Suspended:  records[1],
	}}
	encodedRecord, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("encode run suspension record: %w", err)
	}
	if len(encodedRecord) > engine.MaxPayloadBytes {
		return fmt.Errorf(
			"run suspension records exceed workflow payload limit %d bytes",
			engine.MaxPayloadBytes,
		)
	}
	_, err = r.executeStorageWithRetry(wfCtx.Context(), command)
	return err
}

// prepareRunSuspensionRecords builds the private checkpoint and matching public
// event that the store commits together.
func prepareRunSuspensionRecords(ctx context.Context, input *RunInput, suspension *api.RunSuspension) ([]*RecordActivityInput, error) {
	return prepareRunSuspensionRecordsWithMetadata(
		input,
		suspension,
		recordDispatchMetadataForContext(ctx),
	)
}

// prepareRunSuspensionRecordsAt builds repair records at the time the engine
// completed the workflow.
func prepareRunSuspensionRecordsAt(input *RunInput, suspension *api.RunSuspension, completedAt time.Time) ([]*RecordActivityInput, error) {
	return prepareRunSuspensionRecordsWithMetadata(input, suspension, recordDispatchMetadata{
		EventKey: repairedRunSuspensionEventKey, TimestampMS: completedAt.UnixMilli(),
	})
}

// prepareRunSuspensionRecordsWithMetadata builds both records from one public
// event identity and occurrence time.
func prepareRunSuspensionRecordsWithMetadata(input *RunInput, suspension *api.RunSuspension, meta recordDispatchMetadata) ([]*RecordActivityInput, error) {
	data, err := json.Marshal(suspension)
	if err != nil {
		return nil, fmt.Errorf("encode run suspension for storage: %w", err)
	}
	record := &RecordActivityInput{
		Type:      runSuspensionRecordType,
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: input.SessionID,
		Payload:   data,
	}
	suspendedRecord, err := prepareHookRecordInputWithMetadata(
		hooks.NewRunSuspendedEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			suspension.ID,
			suspension.Version,
			len(suspension.Pending),
			suspension.RequiredTools,
		),
		input.TurnID,
		meta,
	)
	if err != nil {
		return nil, err
	}
	return []*RecordActivityInput{record, suspendedRecord}, nil
}

// decodeRunSuspensionRecord validates the private activity variant and returns
// the exact bytes that must be committed with the suspended record.
func (r *Runtime) decodeRunSuspensionRecord(input *RecordActivityInput) (session.RunSuspension, error) {
	if input.RunID == "" || input.AgentID == "" || input.SessionID == "" || len(input.Payload) == 0 {
		return session.RunSuspension{}, errors.New("runtime: suspension record requires run, agent, session, and payload")
	}
	if input.EventKey != "" || input.TurnID != "" || input.TimestampMS != 0 {
		return session.RunSuspension{}, errors.New("runtime: suspension record cannot contain run-log event fields")
	}
	var suspension api.RunSuspension
	if err := decodeStoredRunSuspension(input.Payload, &suspension); err != nil {
		return session.RunSuspension{}, fmt.Errorf("decode run suspension storage payload: %w", err)
	}
	checkpoint, err := decodeWorkflowCheckpointState(&suspension)
	if err != nil {
		return session.RunSuspension{}, err
	}
	if checkpoint.AgentID != string(input.AgentID) ||
		checkpoint.SessionID != input.SessionID ||
		checkpoint.PreviousRunID != input.RunID {
		return session.RunSuspension{}, errors.New("runtime: suspension record identity does not match checkpoint")
	}
	return session.RunSuspension{
		ID:   suspension.ID,
		Data: append([]byte(nil), input.Payload...),
	}, nil
}

// LoadRunSuspension returns a checkpoint only for a canonically suspended run.
// Other terminal outcomes permanently have no suspension.
func (r *Runtime) LoadRunSuspension(ctx context.Context, runID string) (*api.RunSuspension, error) {
	run, err := r.Store.LoadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	switch run.Status {
	case session.RunStatusSuspended:
	case session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled:
		return nil, session.ErrRunSuspensionNotFound
	case session.RunStatusRunning:
		return nil, fmt.Errorf(
			"%w: run %q has status %q",
			ErrRunSuspensionNotReady,
			runID,
			run.Status,
		)
	default:
		panic("runtime: unsupported session run status: " + string(run.Status))
	}
	stored, err := r.Store.LoadRunSuspension(ctx, runID)
	if errors.Is(err, session.ErrRunSuspensionNotFound) {
		return nil, fmt.Errorf("%w: run %q has no stored checkpoint", ErrRunSuspensionCorrupt, runID)
	}
	if err != nil {
		return nil, err
	}
	var suspension api.RunSuspension
	if err := decodeStoredRunSuspension(stored.Data, &suspension); err != nil {
		return nil, fmt.Errorf(
			"%w: decode run %q checkpoint envelope: %w",
			ErrRunSuspensionCorrupt,
			runID,
			err,
		)
	}
	if suspension.ID != stored.ID {
		return nil, fmt.Errorf(
			"%w: run %q stored checkpoint id does not match payload",
			ErrRunSuspensionCorrupt,
			runID,
		)
	}
	checkpoint, err := decodeWorkflowCheckpointState(&suspension)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: decode run %q checkpoint state: %w",
			ErrRunSuspensionCorrupt,
			runID,
			err,
		)
	}
	if checkpoint.PreviousRunID != runID {
		return nil, fmt.Errorf(
			"%w: stored checkpoint belongs to run %q instead of %q",
			ErrRunSuspensionCorrupt,
			checkpoint.PreviousRunID,
			runID,
		)
	}
	return &suspension, nil
}

// decodeStoredRunSuspension accepts exactly the runtime-owned suspension
// fields. Unknown or trailing data indicates incompatible stored state.
func decodeStoredRunSuspension(data []byte, suspension *api.RunSuspension) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(suspension); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("run suspension has trailing data")
	}
	return nil
}
