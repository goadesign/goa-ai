package runtime

// These fixtures commit a start and close the run before returning its exact
// retry. Runtime tests then exercise real storage without executing agent work.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/stream"
)

type (
	closedStartStore struct {
		storage.Store
		status        session.RunStatus
		ordinaryCalls int
		terminalCalls int
		cancelCalls   int
		closedMeta    session.RunMeta
		closedPage    runlog.Page
	}

	closedStartFixture struct {
		runtime *Runtime
		store   *closedStartStore
		context *routeWorkflowContext
		input   *RunInput
		command *api.StorageActivityCommand
		bus     *recordingHooks
		sink    *recordingStreamSink
	}
)

func (s *closedStartStore) StartRootRun(ctx context.Context, command storage.RootRunStart) (storage.RootRunStartResult, error) {
	if _, err := s.Store.StartRootRun(ctx, command); err != nil {
		return storage.RootRunStartResult{}, err
	}
	if err := s.closeRun(ctx, command.Run.RunID); err != nil {
		return storage.RootRunStartResult{}, err
	}
	return s.Store.StartRootRun(ctx, command)
}

func (s *closedStartStore) StartChildRun(ctx context.Context, command storage.ChildRunStart) (storage.ChildRunStartResult, error) {
	if _, err := s.Store.StartChildRun(ctx, command); err != nil {
		return storage.ChildRunStartResult{}, err
	}
	if err := s.closeRun(ctx, command.Run.RunID); err != nil {
		return storage.ChildRunStartResult{}, err
	}
	return s.Store.StartChildRun(ctx, command)
}

func (s *closedStartStore) StartOneShotRun(ctx context.Context, command storage.OneShotRunStart) (storage.OneShotRunStartResult, error) {
	if _, err := s.Store.StartOneShotRun(ctx, command); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	if err := s.closeRun(ctx, command.Run.RunID); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	return s.Store.StartOneShotRun(ctx, command)
}

func (s *closedStartStore) StartOneShotChildRun(ctx context.Context, command storage.OneShotChildRunStart) (storage.OneShotChildRunStartResult, error) {
	if _, err := s.Store.StartOneShotChildRun(ctx, command); err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	if err := s.closeRun(ctx, command.Run.RunID); err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	return s.Store.StartOneShotChildRun(ctx, command)
}

func (s *closedStartStore) AppendRunRecord(ctx context.Context, record *runlog.Event) (storage.AppendResult, error) {
	s.ordinaryCalls++
	return s.Store.AppendRunRecord(ctx, record)
}

func (s *closedStartStore) RecordRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.AppendResult, error) {
	s.terminalCalls++
	return s.Store.RecordRunTerminal(ctx, command)
}

func (s *closedStartStore) RecordRunCancellation(ctx context.Context, command storage.RunCancellation) (storage.AppendResult, error) {
	s.cancelCalls++
	return s.Store.RecordRunCancellation(ctx, command)
}

// newClosedStartFixture prepares the same workflow-owned command used by the
// runtime. Session children have a real parent in their Session. Workflow tests
// publish initial messages and prompt facts; callback tests let RunOneShot
// publish its own empty history.
func newClosedStartFixture(t *testing.T, kind storageCommandKind, status session.RunStatus, publishHistory bool) closedStartFixture {
	t.Helper()
	store := storageinmem.New()
	input := &RunInput{AgentID: "agent", RunID: "run", TurnID: "turn"}
	if kind == storageCommandRootStart || kind == storageCommandChildStart {
		input.SessionID = "session"
		_, err := store.CreateSession(t.Context(), input.SessionID, time.Unix(1, 0))
		require.NoError(t, err)
	}
	if kind == storageCommandChildStart || kind == storageCommandOneShotChildStart {
		input.ParentRunID, input.ParentAgentID = "parent-run", "parent.agent"
		input.ParentToolCallID, input.Tool = "call", "parent.child"
		admitRunForTest(t, store, session.RunMeta{
			RunID: input.ParentRunID, AgentID: string(input.ParentAgentID), SessionID: input.SessionID,
			Status: session.RunStatusRunning,
		})
	}
	closedStore := &closedStartStore{Store: store, status: status}
	runtime := newTestRuntimeWithPlanner("agent", &stubPlanner{
		start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return nil, errors.New("closed run reached planner")
		},
	})
	runtime.Store = closedStore
	if publishHistory {
		endID, err := publishLiteralHistory(t.Context(), runtime.Store, storage.SeedDeclaration{
			AgentID: string(input.AgentID), RunID: input.RunID, SessionID: input.SessionID,
			Kind:            storage.SeedLiteral,
			RenderedPrompts: []prompt.RenderEvent{{PromptID: "agent.system", Version: "v1"}},
		}, []*model.Message{{
			Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "meaningful input"}},
		}})
		require.NoError(t, err)
		input.SeedEndID = endID
	}
	bus, sink := &recordingHooks{}, &recordingStreamSink{}
	runtime.Bus = bus
	var err error
	runtime.streamSubscriber, err = stream.NewSubscriber(sink, stream.RuntimeHostProfile())
	require.NoError(t, err)
	wfCtx := &routeWorkflowContext{
		ctx: t.Context(), runID: input.RunID, hookRuntime: runtime,
		now: func() time.Time { return time.Unix(2, 0) },
	}
	events := []hooks.Event{}
	if input.ParentRunID != "" {
		events = append(events, hooks.NewChildRunLinkedEvent(
			input.ParentRunID, input.ParentAgentID, input.SessionID, input.Tool,
			input.ParentToolCallID, input.RunID, input.AgentID,
		))
	}
	events = append(events, hooks.NewRunStartedEvent(
		input.RunID, input.AgentID, input.SessionID, input.ParentRunID, "", nil,
	))
	records, err := prepareRunStartRecords(wfCtx.Context(), events, input.TurnID)
	require.NoError(t, err)
	command, err := runStartStorageCommand(input, records)
	require.NoError(t, err)
	return closedStartFixture{runtime, closedStore, wfCtx, input, command, bus, sink}
}

// closeRun records the requested terminal state through the actual Store,
// then saves the complete run and history for later preservation assertions.
func (s *closedStartStore) closeRun(ctx context.Context, runID string) error {
	meta, err := s.LoadRun(ctx, runID)
	if err != nil {
		return err
	}
	if session.IsTerminalRunStatus(meta.Status) {
		return nil
	}
	if s.status == session.RunStatusSuspended {
		err = storeSuspensionForTest(ctx, s.Store, runID, session.RunSuspension{
			ID: "checkpoint", Data: []byte(`{"version":"v6"}`),
		})
	} else {
		status := string(s.status)
		phase := run.PhaseFailed
		var eventErr error
		var cancellation *run.Cancellation
		switch s.status {
		case session.RunStatusRunning, session.RunStatusSuspended:
			return errors.New("fixture requires a completed, failed, or canceled run")
		case session.RunStatusCompleted:
			status, phase = runStatusSuccess, run.PhaseCompleted
		case session.RunStatusFailed:
			eventErr = errors.New("original failure")
		case session.RunStatusCanceled:
			phase, eventErr = run.PhaseCanceled, context.Canceled
			cancellation = &run.Cancellation{Reason: run.CancellationReasonUserRequested}
			payload, encodeErr := json.Marshal(cancellationIntentPayload{Reason: cancellation.Reason})
			if encodeErr != nil {
				return encodeErr
			}
			_, cancelErr := s.Store.RecordRunCancellation(ctx, storage.RunCancellation{
				RunID: runID, Reason: cancellation.Reason,
				Record: &runlog.Event{
					EventKey: cancellationIntentEventKey, RunID: runID, AgentID: "agent",
					SessionID: meta.SessionID, Type: storage.CancellationRecordType,
					Payload: payload, Timestamp: time.Unix(3, 0),
				},
			})
			if cancelErr != nil {
				return cancelErr
			}
		}
		event := hooks.NewRunCompletedEvent(
			runID, "agent", meta.SessionID, status, phase, nil, eventErr, cancellation,
		)
		record, recordErr := encodeTestHookRecord(event, terminalRunEventKey, time.Unix(3, 0))
		if recordErr != nil {
			return recordErr
		}
		_, err = s.Store.RecordRunTerminal(ctx, storage.RunTerminal{
			RunID: runID, Status: s.status, Record: record,
		})
	}
	if err != nil {
		return err
	}
	s.closedMeta, err = s.LoadRun(ctx, runID)
	if err != nil {
		return err
	}
	s.closedPage, err = s.ListRunRecords(ctx, runID, "", 100)
	return err
}

// assertUnchanged proves the attempted execution did not append messages,
// prompts, cancellation intent, or a replacement terminal record.
func (f closedStartFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	assert.Zero(t, f.store.ordinaryCalls)
	assert.Zero(t, f.store.terminalCalls)
	assert.Zero(t, f.store.cancelCalls)
	assert.Empty(t, f.bus.events)
	assert.Empty(t, f.sink.snapshot())
	meta, err := f.store.LoadRun(t.Context(), f.input.RunID)
	require.NoError(t, err)
	assert.Equal(t, f.store.closedMeta, meta)
	page, err := f.store.ListRunRecords(t.Context(), f.input.RunID, "", 100)
	require.NoError(t, err)
	assert.Equal(t, f.store.closedPage, page)
}
