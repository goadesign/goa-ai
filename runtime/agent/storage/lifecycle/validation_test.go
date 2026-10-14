// These tests prove that ordinary history writes cannot bypass the operations
// that store lifecycle records and matching run state together.
package lifecycle

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
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
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("service.agent"))},
		RunID:     "run",
		SessionID: "session",
		StartedAt: startedAt,
	}
	started := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		start.Subject,
		start.SessionID,
		"",
		"",
		nil,
	), startedAt)
	root := storage.RootRunStart{RequestDigest: [32]byte{1}, Run: start, Started: started}
	require.NoError(t, ValidateRootRunStart(root))

	root.Started = cloneLifecycleRecord(started)
	root.Started.Timestamp = startedAt.Add(time.Millisecond)
	require.ErrorContains(t, ValidateRootRunStart(root), "started record: timestamp does not match run start")

	childStart := start
	childStart.RunID = "child"
	childStart.ParentRunID = start.RunID
	childStarted := lifecycleRecord(t, hooks.NewRunStartedEvent(
		childStart.RunID,
		childStart.Subject,
		childStart.SessionID,
		childStart.ParentRunID,
		"",
		nil,
	), startedAt)
	linked := lifecycleRecord(t, hooks.NewChildRunLinkedEvent(
		start.RunID,
		start.Subject,
		start.SessionID,
		"child.tools.run",
		"call-1",
		childStart.RunID,
		subjectAgent(t, childStart.Subject),
	), startedAt.Add(time.Millisecond))
	require.NoError(t, ValidateChildRunStart(storage.ChildRunStart{RequestDigest: [32]byte{1},
		Run: childStart, ParentLinked: linked, Started: childStarted,
	}))
}

func TestValidateOneShotRunStartRequiresMillisecondPrecision(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 1, time.UTC)
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("service.agent"))}, RunID: "run", StartedAt: startedAt}
	started := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		start.Subject,
		"",
		"",
		"",
		nil,
	), startedAt.Truncate(time.Millisecond))

	require.EqualError(
		t,
		ValidateOneShotRunStart(storage.OneShotRunStart{RequestDigest: [32]byte{1}, Run: start, Started: started}),
		"started_at must use millisecond precision",
	)
}

func TestValidateOneShotChildRunStart(t *testing.T) {
	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	parent := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))}, RunID: "parent", StartedAt: startedAt}
	child := session.RunStart{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child.agent"))}, RunID: "child", ParentRunID: parent.RunID, StartedAt: startedAt,
	}
	command := storage.OneShotChildRunStart{RequestDigest: [32]byte{1},
		Run: child,
		ParentLinked: lifecycleRecord(t, hooks.NewChildRunLinkedEvent(
			parent.RunID,
			parent.Subject,
			"",
			"parent.tools.child",
			"call-1",
			child.RunID,
			subjectAgent(t, child.Subject),
		), startedAt),
		Started: lifecycleRecord(t, hooks.NewRunStartedEvent(
			child.RunID,
			child.Subject,
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
	withPredecessor.Started = lifecycleRecord(t, hooks.NewRunStartedEvent(
		child.RunID, child.Subject, "", parent.RunID, "predecessor", nil,
	), startedAt)
	require.NoError(t, ValidateOneShotChildRunStart(withPredecessor))
	withoutParent := command
	withoutParent.Run.ParentRunID = ""
	require.ErrorIs(t, ValidateOneShotChildRunStart(withoutParent), session.ErrParentRunIDRequired)
}

func TestValidateChildRunStartRequiresParentToolIdentity(t *testing.T) {
	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	parent := session.RunStart{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))}, RunID: "parent", SessionID: "session", StartedAt: startedAt,
	}
	child := session.RunStart{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child.agent"))}, RunID: "child", SessionID: parent.SessionID,
		ParentRunID: parent.RunID, StartedAt: startedAt,
	}
	started := lifecycleRecord(t, hooks.NewRunStartedEvent(
		child.RunID, child.Subject, child.SessionID, child.ParentRunID, "", nil,
	), startedAt)

	for _, test := range []struct {
		name       string
		toolName   string
		toolCallID string
	}{
		{name: "missing tool name", toolCallID: "call-1"},
		{name: "missing tool call id", toolName: "parent.tools.child"},
	} {
		t.Run(test.name, func(t *testing.T) {
			linked := lifecycleRecord(t, hooks.NewChildRunLinkedEvent(
				parent.RunID,
				parent.Subject,
				parent.SessionID,
				tools.Ident(test.toolName),
				test.toolCallID,
				child.RunID,
				subjectAgent(t, child.Subject),
			), startedAt)

			err := ValidateChildRunStart(storage.ChildRunStart{RequestDigest: [32]byte{1},
				Run: child, ParentLinked: linked, Started: started,
			})
			require.ErrorContains(t, err, "parent tool name and call id are required")
		})
	}
}

func TestValidateStoredChildLinkRequiresExactStoredParent(t *testing.T) {
	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	parent := session.RunMeta{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))}, RunID: "parent", SessionID: "session",
		StartedAt: startedAt,
	}
	child := session.RunMeta{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child.agent"))}, RunID: "child", SessionID: parent.SessionID,
		ParentRunID: parent.RunID, StartedAt: startedAt,
	}
	linked := lifecycleRecord(t, hooks.NewChildRunLinkedEvent(
		parent.RunID,
		parent.Subject,
		parent.SessionID,
		"parent.tools.child",
		"call-1",
		child.RunID,
		subjectAgent(t, child.Subject),
	), startedAt)
	require.NoError(t, ValidateStoredChildLink(linked, parent, child))

	wrongID := parent
	wrongID.RunID = "other"
	require.ErrorContains(t, ValidateStoredChildLink(linked, wrongID, child), "does not match child parent id")
	wrongSession := parent
	wrongSession.SessionID = "other"
	require.ErrorContains(t, ValidateStoredChildLink(linked, wrongSession, child), "different sessions")
}

func TestValidateRunStartsRejectCancellationEventKey(t *testing.T) {
	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	start := session.RunStart{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("service.agent"))},
		RunID:   "run", SessionID: "session", StartedAt: startedAt,
	}
	started := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID, start.Subject, start.SessionID, "", "", nil,
	), startedAt)
	started.EventKey = "runtime/cancellation-intent"
	require.ErrorContains(t, ValidateRootRunStart(storage.RootRunStart{RequestDigest: [32]byte{1},
		Run: start, Started: started,
	}), "cancellation")
	child := start
	child.RunID, child.ParentRunID = "child", start.RunID
	childStarted := lifecycleRecord(t, hooks.NewRunStartedEvent(
		child.RunID, child.Subject, child.SessionID, child.ParentRunID, "", nil,
	), startedAt)
	childStarted.EventKey = "runtime/cancellation-intent"
	require.ErrorContains(t, ValidateChildRunStart(storage.ChildRunStart{RequestDigest: [32]byte{1},
		Run: child,
		ParentLinked: lifecycleRecord(t, hooks.NewChildRunLinkedEvent(
			start.RunID, start.Subject, start.SessionID, "service.tools.child", "call-1", child.RunID, subjectAgent(t, child.Subject),
		), startedAt),
		Started: childStarted,
	}), "cancellation")
}

func TestStartCancellationUsesAcceptedIdentityAndTime(t *testing.T) {
	started := validOrdinaryRecord()
	started.SessionID, started.TurnID = "session", "turn"
	for _, reason := range []string{"session_ended", "user_requested", "engine_canceled"} {
		t.Run(reason, func(t *testing.T) {
			canceled, err := StartCancellation(started, reason)
			require.NoError(t, err)
			require.Equal(t, "runtime/cancellation-intent", canceled.EventKey)
			require.Equal(t, started.RunID, canceled.RunID)
			require.Equal(t, started.Subject, canceled.Subject)
			require.Equal(t, started.SessionID, canceled.SessionID)
			require.Equal(t, started.TurnID, canceled.TurnID)
			require.Equal(t, started.Timestamp, canceled.Timestamp)
			require.Equal(t, storage.CancellationRecordType, canceled.Type)
			require.JSONEq(t, `{"reason":"`+reason+`"}`, string(canceled.Payload))
		})
	}
	_, err := StartCancellation(started, "")
	require.ErrorContains(t, err, "cancellation reason is required")
}

func TestValidateRunStartPredecessor(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	start := session.RunStart{
		Subject:          genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("service.agent"))},
		RunID:            "run",
		SessionID:        "session",
		PredecessorRunID: "predecessor",
		StartedAt:        startedAt,
	}
	continued := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		start.Subject,
		start.SessionID,
		"",
		"predecessor",
		nil,
	), startedAt)
	require.NoError(t, ValidateRootRunStart(storage.RootRunStart{RequestDigest: [32]byte{1},
		Run: start, Started: continued,
	}))
	mismatched := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		start.Subject,
		start.SessionID,
		"",
		"another-predecessor",
		nil,
	), startedAt)
	require.ErrorContains(t, ValidateRootRunStart(storage.RootRunStart{RequestDigest: [32]byte{1},
		Run: start, Started: mismatched,
	}), "predecessor run id does not match run")

	selfStart := start
	selfStart.PredecessorRunID = start.RunID
	self := lifecycleRecord(t, hooks.NewRunStartedEvent(
		start.RunID,
		start.Subject,
		start.SessionID,
		"",
		start.RunID,
		nil,
	), startedAt)
	require.ErrorContains(t, ValidateRootRunStart(storage.RootRunStart{RequestDigest: [32]byte{1},
		Run: selfStart, Started: self,
	}), "predecessor run id must differ from run id")

	oneShot := start
	oneShot.SessionID = ""
	oneShot.PredecessorRunID = "predecessor"
	oneShotStarted := lifecycleRecord(t, hooks.NewRunStartedEvent(
		oneShot.RunID,
		oneShot.Subject,
		"",
		"",
		"predecessor",
		nil,
	), startedAt)
	require.NoError(t, ValidateOneShotRunStart(storage.OneShotRunStart{RequestDigest: [32]byte{1},
		Run: oneShot, Started: oneShotStarted,
	}))
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
		EventKey: input.EventKey, RunID: input.RunID, Subject: input.Subject,
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
		Subject:  genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("service.agent"))},
		Type:     runlog.Type("planner_note"),
		Payload:  []byte(`{"value":1}`),
		Timestamp: time.Date(
			2026, time.August, 29, 12, 0, 0, 0, time.UTC,
		),
	}
}
