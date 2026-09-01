// Package storage defines the durable state changes used by agent workflows.
//
// A Store owns run metadata, continuation checkpoints, and ordered run records.
// Each lifecycle method writes the state change and its records together. Host
// applications own session creation, ending, and permanent deletion outside
// this worker-facing contract.
package storage

import (
	"context"
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
)

type (
	// ContractError reports a command that cannot succeed because it conflicts
	// with stored state or violates the Store contract. Activity runners must not
	// retry it. Cause remains available through errors.Is and errors.As.
	ContractError struct {
		cause error
	}

	// Store persists the complete runtime history needed by agent workflows.
	// Mutation methods return ContractError when retrying the same command cannot
	// change the result. Implementations use lifecycle.ValidateOrdinaryRunRecord
	// for ordinary appends and the other storage/lifecycle validators for
	// lifecycle changes.
	//
	// StartRootRun and StartChildRun accept a continuation when
	// Run.PredecessorRunID is set. Before either method writes the successor run,
	// its RunStarted record, or a child parent link, the predecessor must exist,
	// be suspended, and have the same session ID, agent ID, and parent run ID as
	// the successor. A failed check writes nothing. RunStarted stores the
	// predecessor run ID; implementations must not copy it into RunMeta or guess
	// it from record order or timestamps.
	// Temporary database and network failures remain unwrapped.
	Store interface {
		// StartRootRun records the first state and record for a session root run.
		StartRootRun(context.Context, RootRunStart) (RootRunStartResult, error)
		// StartChildRun records the parent link and first child state together.
		StartChildRun(context.Context, ChildRunStart) (ChildRunStartResult, error)
		// StartOneShotRun records a sessionless run before it does any work.
		StartOneShotRun(context.Context, OneShotRunStart) (OneShotRunStartResult, error)
		// StartOneShotChildRun records a sessionless parent link and child start together.
		StartOneShotChildRun(context.Context, OneShotChildRunStart) (OneShotChildRunStartResult, error)
		// AppendRunRecord stores an ordinary record without changing run state.
		AppendRunRecord(context.Context, *runlog.Event) (AppendResult, error)
		// RecordRunCancellation stores the first cancellation reason and record.
		RecordRunCancellation(context.Context, RunCancellation) (AppendResult, error)
		// RecordRunSuspension stores the checkpoint, record, and suspended state.
		RecordRunSuspension(context.Context, RunSuspension) (AppendResult, error)
		// RecordRunTerminal stores a terminal record and terminal state.
		RecordRunTerminal(context.Context, RunTerminal) (AppendResult, error)
		// RepairRunSuspension stores a missing suspension only while the run is
		// active. Its result distinguishes an exact retry from another terminal
		// record that already owns the run.
		RepairRunSuspension(context.Context, RunSuspension) (RunRepairResult, error)
		// RepairRunTerminal stores a missing terminal result only while the run is
		// active. Its result distinguishes an exact retry from another terminal
		// record that already owns the run.
		RepairRunTerminal(context.Context, RunTerminal) (RunRepairResult, error)
		// LoadRun returns durable lifecycle facts for one run.
		LoadRun(context.Context, string) (session.RunMeta, error)
		// LoadRunSuspension returns the exact checkpoint stored for one run.
		LoadRunSuspension(context.Context, string) (session.RunSuspension, error)
		// ListRunRecords returns one ordered page for a run.
		ListRunRecords(context.Context, string, string, int) (runlog.Page, error)
		// ListSessionRunRecords returns one ordered page across a session.
		ListSessionRunRecords(context.Context, string, string, int) (runlog.Page, error)
	}

	// RootRunStart contains the records stored for a session root run.
	RootRunStart struct {
		// Run is the immutable identity of the accepted workflow.
		Run session.RunStart
		// Started is stored for every accepted workflow.
		Started *runlog.Event
		// Canceled is stored after Started when the session has ended.
		Canceled *runlog.Event
	}

	// ChildRunStart contains the parent link and child lifecycle records.
	ChildRunStart struct {
		// Run is the immutable identity of the accepted child workflow.
		Run session.RunStart
		// ParentLinked records which parent accepted the child.
		ParentLinked *runlog.Event
		// Started is stored after ParentLinked for every accepted workflow.
		Started *runlog.Event
		// Canceled is stored after Started when the session has ended.
		Canceled *runlog.Event
	}

	// OneShotRunStart contains the identity and first record for a sessionless
	// workflow.
	OneShotRunStart struct {
		// Run is the immutable identity of the accepted workflow. SessionID and
		// ParentRunID must be empty.
		Run session.RunStart
		// Started is the first durable record.
		Started *runlog.Event
	}

	// OneShotChildRunStart contains the relationship and start records for a
	// sessionless child workflow.
	OneShotChildRunStart struct {
		// Run is the immutable child identity. SessionID and PredecessorRunID must
		// be empty, and ParentRunID must name an existing sessionless run.
		Run session.RunStart
		// ParentLinked records which parent tool call accepted the child.
		ParentLinked *runlog.Event
		// Started is stored on the child after ParentLinked.
		Started *runlog.Event
	}

	// RunCancellation stores one immutable cancellation reason and its record.
	RunCancellation struct {
		// RunID identifies the run being canceled.
		RunID string
		// Reason records why cancellation was requested.
		Reason string
		// Record is the durable cancellation-intent record.
		Record *runlog.Event
	}

	// RunSuspension stores one checkpoint and the record that makes it usable.
	RunSuspension struct {
		// RunID identifies the suspended run.
		RunID string
		// Suspension contains the opaque continuation checkpoint.
		Suspension session.RunSuspension
		// Record is the terminal suspended record.
		Record *runlog.Event
	}

	// RunTerminal stores one final status and its matching record.
	RunTerminal struct {
		// RunID identifies the terminal run.
		RunID string
		// Status is completed, failed, or canceled.
		Status session.RunStatus
		// Record is the matching terminal record.
		Record *runlog.Event
	}

	// RunRepairOutcome reports which record owns the terminal state observed by
	// a repair operation.
	RunRepairOutcome string

	// RunRepairResult reports whether a repair record was newly stored, already
	// stored by an exact earlier call, or rejected because another record owns the
	// terminal state.
	RunRepairResult struct {
		// Outcome identifies the record that owns the terminal state.
		Outcome RunRepairOutcome
		// Status is the terminal state stored for the run.
		Status session.RunStatus
		// Record identifies the supplied repair record when Outcome is
		// RunRepairStored or RunRepairAlreadyStored.
		Record AppendResult
	}

	// RootRunStartResult reports the immutable root-run decision and records.
	RootRunStartResult struct {
		// Outcome tells the workflow whether it may do work.
		Outcome session.RunStartOutcome
		// Started is the run-started record stored for every outcome.
		Started AppendResult
		// Canceled is the run-completed record stored only when Outcome is stop.
		Canceled AppendResult
	}

	// ChildRunStartResult reports the immutable child-run decision and the two
	// records stored by the operation.
	ChildRunStartResult struct {
		// Outcome tells the child workflow whether it may do work.
		Outcome session.RunStartOutcome
		// ParentRecord is the child-link record stored on the parent run.
		ParentRecord AppendResult
		// Started is the child run-started record stored for every outcome.
		Started AppendResult
		// Canceled is the child run-completed record stored only when Outcome is stop.
		Canceled AppendResult
	}

	// OneShotRunStartResult reports the first record for a sessionless run.
	OneShotRunStartResult struct {
		// Record is the run-started record.
		Record AppendResult
	}

	// OneShotChildRunStartResult reports the two records stored for a
	// sessionless child.
	OneShotChildRunStartResult struct {
		// ParentRecord is the child-link record stored on the parent run.
		ParentRecord AppendResult
		// Started is the run-started record stored on the child run.
		Started AppendResult
	}

	// AppendResult reports the stored record and session state observed in the
	// same write. SessionStatus is empty for sessionless runs.
	AppendResult struct {
		// ID is the store-assigned ordered record identifier.
		ID string
		// Inserted reports whether this call inserted the record.
		Inserted bool
		// SessionStatus is the owning session state observed during the write.
		SessionStatus session.SessionStatus
	}
)

const (
	// CancellationRecordType identifies the record that stores the first
	// accepted cancellation reason for a run.
	CancellationRecordType runlog.Type = "runtime.cancellation_intent"

	// RunRepairStored means this call stored the supplied repair record and
	// terminal state. Record.Inserted is true.
	RunRepairStored RunRepairOutcome = "stored"
	// RunRepairAlreadyStored means an earlier call stored this exact repair
	// record. Record identifies that write and Inserted is false.
	RunRepairAlreadyStored RunRepairOutcome = "already_stored"
	// RunRepairDifferentTerminal means another record owns the terminal state and
	// the supplied repair record was not stored.
	RunRepairDifferentTerminal RunRepairOutcome = "different_terminal"
)

var (
	// ErrRunRecordOwnerMismatch means a record names a different agent or
	// session than the stored run.
	ErrRunRecordOwnerMismatch = errors.New("run record owner does not match stored run")
)

// NewContractError marks cause as a permanent Store contract rejection.
func NewContractError(cause error) *ContractError {
	return &ContractError{cause: cause}
}

// ValidateRunRecord checks the fields every Store requires before it can
// assign an ordered identifier and persist the record.
func ValidateRunRecord(record *runlog.Event) error {
	switch {
	case record == nil:
		return errors.New("run record is required")
	case record.RunID == "":
		return errors.New("run id is required")
	case record.AgentID == "":
		return errors.New("agent id is required")
	case record.EventKey == "":
		return errors.New("event key is required")
	case record.Type == "":
		return errors.New("record type is required")
	case len(record.Payload) == 0:
		return errors.New("record payload is required")
	case record.Timestamp.IsZero():
		return errors.New("record timestamp is required")
	case !record.Timestamp.Equal(record.Timestamp.Truncate(time.Millisecond)):
		return errors.New("record timestamp must use millisecond precision")
	default:
		return nil
	}
}

// Error implements error.
func (e *ContractError) Error() string {
	return e.cause.Error()
}

// Unwrap returns the specific contract failure.
func (e *ContractError) Unwrap() error {
	return e.cause
}
