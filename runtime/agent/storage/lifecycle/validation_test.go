// These tests prove that ordinary history writes cannot bypass the operations
// that store lifecycle records and matching run state together.
package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestValidateOrdinaryRunRecordRejectsLifecycleTypes(t *testing.T) {
	t.Parallel()

	for _, recordType := range []runlog.Type{
		hooks.RunStarted,
		hooks.ChildRunLinked,
		storage.CancellationRecordType,
		hooks.RunSuspended,
		hooks.RunCompleted,
	} {
		t.Run(string(recordType), func(t *testing.T) {
			t.Parallel()
			record := validOrdinaryRecord()
			record.Type = recordType

			require.EqualError(
				t,
				ValidateOrdinaryRunRecord(record),
				`record type "`+string(recordType)+`" requires a lifecycle operation`,
			)
		})
	}

	require.NoError(t, ValidateOrdinaryRunRecord(validOrdinaryRecord()))
}

func TestValidateRunStartsRequireMatchingTimestamps(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	start := session.RunStart{
		AgentID:   "service.agent",
		RunID:     "run",
		SessionID: "session",
		StartedAt: startedAt,
	}
	started := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		"",
		"",
		nil,
	), startedAt)
	canceled := lifecycleRecord(t, hooks.NewRunCompletedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		"canceled",
		run.PhaseCanceled,
		nil,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonSessionEnded},
	), startedAt)
	root := storage.RootRunStart{Run: start, Started: started, Canceled: canceled}
	require.NoError(t, ValidateRootRunStart(root))

	root.Started = cloneLifecycleRecord(started)
	root.Started.Timestamp = startedAt.Add(time.Millisecond)
	require.ErrorContains(t, ValidateRootRunStart(root), "started record: timestamp does not match run start")
	root.Started = started
	root.Canceled = cloneLifecycleRecord(canceled)
	root.Canceled.Timestamp = startedAt.Add(time.Millisecond)
	require.ErrorContains(t, ValidateRootRunStart(root), "canceled record: timestamp does not match run start")

	childStart := start
	childStart.RunID = "child"
	childStart.ParentRunID = start.RunID
	childStarted := lifecycleRecord(t, hooks.NewRunStartedEvent(
		childStart.RunID,
		agent.Ident(childStart.AgentID),
		childStart.SessionID,
		childStart.ParentRunID,
		"",
		nil,
	), startedAt)
	childCanceled := lifecycleRecord(t, hooks.NewRunCompletedEvent(
		childStart.RunID,
		agent.Ident(childStart.AgentID),
		childStart.SessionID,
		"canceled",
		run.PhaseCanceled,
		nil,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonSessionEnded},
	), startedAt)
	linked := lifecycleRecord(t, hooks.NewChildRunLinkedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		"child.tools.run",
		"call-1",
		childStart.RunID,
		agent.Ident(childStart.AgentID),
	), startedAt.Add(time.Millisecond))
	require.NoError(t, ValidateChildRunStart(storage.ChildRunStart{
		Run: childStart, ParentLinked: linked, Started: childStarted, Canceled: childCanceled,
	}))
}

func TestValidateOneShotRunStartRequiresMillisecondPrecision(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 1, time.UTC)
	start := session.RunStart{AgentID: "service.agent", RunID: "run", StartedAt: startedAt}
	started := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		"",
		"",
		"",
		nil,
	), startedAt.Truncate(time.Millisecond))

	require.EqualError(
		t,
		ValidateOneShotRunStart(storage.OneShotRunStart{Run: start, Started: started}),
		"started_at must use millisecond precision",
	)
}

func TestValidateOneShotChildRunStart(t *testing.T) {
	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	parent := session.RunStart{AgentID: "parent.agent", RunID: "parent", StartedAt: startedAt}
	child := session.RunStart{
		AgentID: "child.agent", RunID: "child", ParentRunID: parent.RunID, StartedAt: startedAt,
	}
	command := storage.OneShotChildRunStart{
		Run: child,
		ParentLinked: lifecycleRecord(t, hooks.NewChildRunLinkedEvent(
			parent.RunID,
			agent.Ident(parent.AgentID),
			"",
			"parent.tools.child",
			"call-1",
			child.RunID,
			agent.Ident(child.AgentID),
		), startedAt),
		Started: lifecycleRecord(t, hooks.NewRunStartedEvent(
			child.RunID,
			agent.Ident(child.AgentID),
			"",
			parent.RunID,
			"",
			nil,
		), startedAt),
	}
	require.NoError(t, ValidateOneShotChildRunStart(command))
	colliding := command
	colliding.Started = cloneLifecycleRecord(command.Started)
	colliding.Started.EventKey = command.ParentLinked.EventKey
	require.ErrorContains(t, ValidateOneShotChildRunStart(colliding), "require different event keys")

	withSession := command
	withSession.Run.SessionID = "session"
	require.ErrorContains(t, ValidateOneShotChildRunStart(withSession), "cannot have session id")
	withPredecessor := command
	withPredecessor.Run.PredecessorRunID = "predecessor"
	require.ErrorContains(t, ValidateOneShotChildRunStart(withPredecessor), "cannot have predecessor")
	withoutParent := command
	withoutParent.Run.ParentRunID = ""
	require.ErrorIs(t, ValidateOneShotChildRunStart(withoutParent), session.ErrParentRunIDRequired)
}

func TestValidateSessionRunStartRejectsStartedAndCanceledKeyCollision(t *testing.T) {
	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	start := session.RunStart{
		AgentID: "service.agent", RunID: "run", SessionID: "session", StartedAt: startedAt,
	}
	started := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID, agent.Ident(start.AgentID), start.SessionID, "", "", nil,
	), startedAt)
	canceled := lifecycleRecord(t, hooks.NewRunCompletedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		"canceled",
		run.PhaseCanceled,
		nil,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonSessionEnded},
	), startedAt)
	canceled.EventKey = started.EventKey

	require.ErrorContains(t, ValidateRootRunStart(storage.RootRunStart{
		Run: start, Started: started, Canceled: canceled,
	}), "require different event keys")
	child := start
	child.RunID = "child"
	child.ParentRunID = start.RunID
	childStarted := lifecycleRecord(t, hooks.NewRunStartedEvent(
		child.RunID, agent.Ident(child.AgentID), child.SessionID, child.ParentRunID, "", nil,
	), startedAt)
	childCanceled := lifecycleRecord(t, hooks.NewRunCompletedEvent(
		child.RunID,
		agent.Ident(child.AgentID),
		child.SessionID,
		"canceled",
		run.PhaseCanceled,
		nil,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonSessionEnded},
	), startedAt)
	childCanceled.EventKey = childStarted.EventKey
	require.ErrorContains(t, ValidateChildRunStart(storage.ChildRunStart{
		Run: child,
		ParentLinked: lifecycleRecord(t, hooks.NewChildRunLinkedEvent(
			start.RunID,
			agent.Ident(start.AgentID),
			start.SessionID,
			"service.tools.child",
			"call-1",
			child.RunID,
			agent.Ident(child.AgentID),
		), startedAt),
		Started:  childStarted,
		Canceled: childCanceled,
	}), "require different event keys")
}

func TestValidateRunStartPredecessor(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	start := session.RunStart{
		AgentID:          "service.agent",
		RunID:            "run",
		SessionID:        "session",
		PredecessorRunID: "predecessor",
		StartedAt:        startedAt,
	}
	canceled := lifecycleRecord(t, hooks.NewRunCompletedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		"canceled",
		run.PhaseCanceled,
		nil,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonSessionEnded},
	), startedAt)
	continued := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		"",
		"predecessor",
		nil,
	), startedAt)
	require.NoError(t, ValidateRootRunStart(storage.RootRunStart{
		Run: start, Started: continued, Canceled: canceled,
	}))
	mismatched := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		"",
		"another-predecessor",
		nil,
	), startedAt)
	require.ErrorContains(t, ValidateRootRunStart(storage.RootRunStart{
		Run: start, Started: mismatched, Canceled: canceled,
	}), "predecessor run id does not match run")

	selfStart := start
	selfStart.PredecessorRunID = start.RunID
	self := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		"",
		start.RunID,
		nil,
	), startedAt)
	require.ErrorContains(t, ValidateRootRunStart(storage.RootRunStart{
		Run: selfStart, Started: self, Canceled: canceled,
	}), "predecessor run id must differ from run id")

	oneShot := start
	oneShot.SessionID = ""
	oneShot.PredecessorRunID = "predecessor"
	oneShotStarted := lifecycleRecord(t, hooks.NewRunStartedEvent(
		oneShot.RunID,
		agent.Ident(oneShot.AgentID),
		"",
		"",
		"predecessor",
		nil,
	), startedAt)
	require.ErrorContains(t, ValidateOneShotRunStart(storage.OneShotRunStart{
		Run: oneShot, Started: oneShotStarted,
	}), "one-shot run cannot have predecessor run id")
}

// lifecycleRecord encodes one typed event as the immutable record accepted by
// lifecycle validation.
func lifecycleRecord(t *testing.T, event hooks.Event, timestamp time.Time) *runlog.Event {
	t.Helper()
	input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{
		EventKey: string(event.Type()), TimestampMS: timestamp.UnixMilli(),
	})
	require.NoError(t, err)
	return &runlog.Event{
		EventKey: input.EventKey, RunID: input.RunID, AgentID: input.AgentID,
		SessionID: input.SessionID, TurnID: input.TurnID, Type: input.Type,
		Payload: input.Payload, Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	}
}

// cloneLifecycleRecord copies one record before a test changes an immutable
// field.
func cloneLifecycleRecord(record *runlog.Event) *runlog.Event {
	cloned := *record
	cloned.Payload = append([]byte(nil), record.Payload...)
	return &cloned
}

// validOrdinaryRecord returns the smallest complete non-lifecycle record.
func validOrdinaryRecord() *runlog.Event {
	return &runlog.Event{
		EventKey: "event",
		RunID:    "run",
		AgentID:  agent.Ident("service.agent"),
		Type:     runlog.Type("planner_note"),
		Payload:  []byte(`{"value":1}`),
		Timestamp: time.Date(
			2026, time.August, 29, 12, 0, 0, 0, time.UTC,
		),
	}
}
