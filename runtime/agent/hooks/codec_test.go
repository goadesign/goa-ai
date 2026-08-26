package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	testRunID     = "run-1"
	testSessionID = "session-1"
)

func TestToolCallScheduledCodecPreservesContinuationRoot(t *testing.T) {
	t.Parallel()

	event := NewToolCallScheduledEvent(
		"run-1",
		"agent-1",
		"session-1",
		tools.Ident("tools.continue_search"),
		"continue-1",
		rawjson.Message(`{"cursor":"next"}`),
		"tools",
		"",
		0,
	)
	event.ContinuationRootToolCallID = "source-1"

	record, err := EncodeToRecordInput(event, EncodeOptions{
		EventKey:    "event-1",
		TimestampMS: 1,
	})
	require.NoError(t, err)
	decoded, err := DecodeFromRecordInput(record)
	require.NoError(t, err)

	scheduled, ok := decoded.(*ToolCallScheduledEvent)
	require.True(t, ok)
	assert.Equal(t, "source-1", scheduled.ContinuationRootToolCallID)
}

func TestDecodeRunlogEventPreservesDurableEnvelope(t *testing.T) {
	t.Parallel()

	event := NewToolCallScheduledEvent(
		"run-1",
		"agent-1",
		"session-1",
		tools.Ident("tools.lookup"),
		"call-1",
		rawjson.Message(`{"query":"status"}`),
		"tools",
		"",
		0,
	)
	record, err := EncodeToRecordInput(event, EncodeOptions{
		TurnID:      "turn-1",
		EventKey:    "event-1",
		TimestampMS: 1234,
	})
	require.NoError(t, err)

	decoded, err := DecodeRunlogEvent(&runlog.Event{
		ID:        "opaque-cursor",
		EventKey:  record.EventKey,
		RunID:     record.RunID,
		AgentID:   record.AgentID,
		SessionID: record.SessionID,
		TurnID:    record.TurnID,
		Type:      record.Type,
		Payload:   record.Payload,
		Timestamp: time.UnixMilli(record.TimestampMS).UTC(),
	})

	require.NoError(t, err)
	scheduled, ok := decoded.(*ToolCallScheduledEvent)
	require.True(t, ok)
	assert.Equal(t, record.EventKey, scheduled.EventKey())
	assert.Equal(t, record.RunID, scheduled.RunID())
	assert.Equal(t, record.SessionID, scheduled.SessionID())
	assert.Equal(t, record.TurnID, scheduled.TurnID())
	assert.Equal(t, record.TimestampMS, scheduled.Timestamp())
	assert.Equal(t, tools.Ident("tools.lookup"), scheduled.ToolName)
}

func TestDecodeRunlogEventRejectsNil(t *testing.T) {
	t.Parallel()

	_, err := DecodeRunlogEvent(nil)

	require.EqualError(t, err, "decode runlog hook event: event is nil")
}

func TestModelOutputRejectedCodecPreservesBoundedResponseFingerprint(t *testing.T) {
	t.Parallel()

	const (
		reasonDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		modelDigest  = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)
	event, err := NewModelOutputRejectedEvent(
		testRunID,
		"agent-1",
		testSessionID,
		reasonDigest,
		47,
		true,
		api.ModelResponseFingerprintVersionV1,
		modelDigest,
		42,
	)
	require.NoError(t, err)

	record, err := EncodeToRecordInput(event, EncodeOptions{
		EventKey:    "event-1",
		TimestampMS: 1,
	})
	require.NoError(t, err)
	decoded, err := DecodeFromRecordInput(record)
	require.NoError(t, err)

	rejected, ok := decoded.(*ModelOutputRejectedEvent)
	require.True(t, ok)
	assert.Equal(t, reasonDigest, rejected.ReasonSHA256)
	assert.EqualValues(t, 47, rejected.ReasonSize)
	assert.True(t, rejected.ModelResponsePresent)
	assert.Equal(t, api.ModelResponseFingerprintVersionV1, rejected.ModelResponseFingerprintVersion)
	assert.Equal(t, modelDigest, rejected.ModelResponseSHA256)
	assert.EqualValues(t, 42, rejected.ModelResponseSize)
}

func TestNewModelOutputRejectedEventRepresentsAbsentCompleteResponse(t *testing.T) {
	const reasonDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	event, err := NewModelOutputRejectedEvent(
		testRunID,
		"agent-1",
		testSessionID,
		reasonDigest,
		47,
		false,
		"",
		"",
		0,
	)

	require.NoError(t, err)
	assert.False(t, event.ModelResponsePresent)
	assert.Empty(t, event.ModelResponseFingerprintVersion)
	assert.Empty(t, event.ModelResponseSHA256)
	assert.Zero(t, event.ModelResponseSize)
}

func TestNewModelOutputRejectedEventRequiresVersionExactlyWithDigest(t *testing.T) {
	const (
		reasonDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		modelDigest  = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)
	tests := []struct {
		name    string
		version string
		digest  string
		size    int64
	}{
		{
			name:    "version without digest",
			version: api.ModelResponseFingerprintVersionV1,
		},
		{
			name:   "digest without version",
			digest: modelDigest,
			size:   42,
		},
		{
			name:    "digest with zero response size",
			version: api.ModelResponseFingerprintVersionV1,
			digest:  modelDigest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, err := NewModelOutputRejectedEvent(
				testRunID,
				"agent-1",
				testSessionID,
				reasonDigest,
				47,
				true,
				test.version,
				test.digest,
				test.size,
			)

			require.Nil(t, event)
			require.Error(t, err)
		})
	}
}

func TestPlannerOutputRejectedCodecPreservesBoundedReason(t *testing.T) {
	const reasonDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	event, err := NewPlannerOutputRejectedEvent(
		testRunID,
		"agent-1",
		testSessionID,
		reasonDigest,
		47,
	)
	require.NoError(t, err)
	record, err := EncodeToRecordInput(event, EncodeOptions{
		EventKey:    "event-1",
		TimestampMS: 1,
	})
	require.NoError(t, err)

	decoded, err := DecodeFromRecordInput(record)

	require.NoError(t, err)
	rejected, ok := decoded.(*PlannerOutputRejectedEvent)
	require.True(t, ok)
	assert.Equal(t, reasonDigest, rejected.ReasonSHA256)
	assert.EqualValues(t, 47, rejected.ReasonSize)
}

func TestPlannerOutputRejectedAcceptsEmptyReasonFingerprint(t *testing.T) {
	const emptyReasonDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	event, err := NewPlannerOutputRejectedEvent(
		testRunID,
		"agent-1",
		testSessionID,
		emptyReasonDigest,
		0,
	)

	require.NoError(t, err)
	assert.Equal(t, emptyReasonDigest, event.ReasonSHA256)
	assert.EqualValues(t, 0, event.ReasonSize)
}

func TestPlannerOutputRejectedRejectsFalseEmptyReasonFingerprint(t *testing.T) {
	_, err := NewPlannerOutputRejectedEvent(
		testRunID,
		"agent-1",
		testSessionID,
		strings.Repeat("0", 64),
		0,
	)

	require.EqualError(
		t,
		err,
		"planner output rejected event reason: SHA-256 digest does not identify an empty reason",
	)
}

func TestDecodeFromRecordInput_ToolResultReceivedPreservesServerDataBytes(t *testing.T) {
	agentID := agent.Ident("agent-1")
	toolName := tools.Ident("svc.tools.lookup")
	toolCallID := "call-1"

	resultJSON := rawjson.Message([]byte(`{"summary":"ok"}`))
	serverData := rawjson.Message([]byte(`[{"kind":"example.topology","data":{"hello":"world","n":1}}]`))

	ev := NewToolResultReceivedEvent(
		testRunID,
		agentID,
		testSessionID,
		"call-run",
		toolName,
		toolCallID,
		"",
		resultJSON,
		len(resultJSON),
		false,
		"",
		serverData,
		"preview",
		nil,
		250*time.Millisecond,
		nil,
		nil,
	)

	in, err := EncodeToRecordInput(ev, EncodeOptions{
		EventKey:    "evt-tool-result",
		TimestampMS: 101,
	})
	require.NoError(t, err)
	require.NotContains(t, string(in.Payload), `"result":`)

	decoded, err := DecodeFromRecordInput(in)
	require.NoError(t, err)

	tr, ok := decoded.(*ToolResultReceivedEvent)
	require.True(t, ok)
	require.Equal(t, toolName, tr.ToolName)
	require.Equal(t, "call-run", tr.CallRunID)
	require.Equal(t, toolCallID, tr.ToolCallID)
	require.Equal(t, len(resultJSON), tr.ResultBytes)
	require.False(t, tr.ResultOmitted)
	require.Empty(t, tr.ResultOmittedReason)
	require.JSONEq(t, string(serverData), string(tr.ServerData))
}

func TestDecodeFromRecordInput_PromptRenderedRoundTrip(t *testing.T) {
	agentID := agent.Ident("agent-1")

	ev := NewPromptRenderedEvent(
		testRunID,
		agentID,
		testSessionID,
		"example.agent.system",
		"v3",
		prompt.Scope{
			SessionID: testSessionID,
			Labels: map[string]string{
				"account": "acme",
				"region":  "west",
			},
		},
	)

	in, err := EncodeToRecordInput(ev, EncodeOptions{
		TurnID:      "turn-1",
		EventKey:    "evt-prompt-rendered",
		TimestampMS: 102,
	})
	require.NoError(t, err)

	decoded, err := DecodeFromRecordInput(in)
	require.NoError(t, err)

	got, ok := decoded.(*PromptRenderedEvent)
	require.True(t, ok)
	require.Equal(t, testRunID, got.RunID())
	require.Equal(t, string(agentID), got.AgentID())
	require.Equal(t, testSessionID, got.SessionID())
	require.Equal(t, "turn-1", got.TurnID())
	require.Equal(t, prompt.Ident("example.agent.system"), got.PromptID)
	require.Equal(t, "v3", got.Version)
	require.Equal(t, testSessionID, got.Scope.SessionID)
	require.Equal(t, "acme", got.Scope.Labels["account"])
	require.Equal(t, "west", got.Scope.Labels["region"])
}

func TestEncodeToRecordInputPreservesDispatchMetadata(t *testing.T) {
	agentID := agent.Ident("agent-1")

	ev := NewPromptRenderedEvent(
		testRunID,
		agentID,
		testSessionID,
		"example.agent.system",
		"v3",
		prompt.Scope{SessionID: testSessionID},
	)

	in, err := EncodeToRecordInput(ev, EncodeOptions{
		TurnID:      "turn-1",
		EventKey:    "evt-prompt-rendered",
		TimestampMS: 103,
	})
	require.NoError(t, err)
	require.Equal(t, int64(103), in.TimestampMS)
	require.Equal(t, "evt-prompt-rendered", in.EventKey)

	decoded, err := DecodeFromRecordInput(in)
	require.NoError(t, err)
	require.Equal(t, int64(103), decoded.Timestamp())
	require.Equal(t, "evt-prompt-rendered", decoded.EventKey())
}

func TestDecodeFromRecordInput_RunCompletedRoundTripsLabels(t *testing.T) {
	labels := map[string]string{
		"household_id": "house-42",
		"source":       "email",
	}
	ev := NewRunCompletedEvent(testRunID, agent.Ident("agent-1"), testSessionID, "success", run.PhaseCompleted, labels, nil, nil)

	in, err := EncodeToRecordInput(ev, EncodeOptions{
		EventKey:    "evt-run-completed-labels",
		TimestampMS: 104,
	})
	require.NoError(t, err)

	decoded, err := DecodeFromRecordInput(in)
	require.NoError(t, err)

	got, ok := decoded.(*RunCompletedEvent)
	require.True(t, ok)
	require.Equal(t, labels, got.Labels)
}

func TestDecodeFromRecordInputRunSuspendedRoundTrip(t *testing.T) {
	event := NewRunSuspendedEvent(
		testRunID,
		agent.Ident("agent-1"),
		testSessionID,
		"suspension-1",
		"v1",
		2,
		[]tools.Ident{"svc.read", "svc.write"},
	)
	record, err := EncodeToRecordInput(event, EncodeOptions{
		TurnID:      "turn-1",
		EventKey:    "evt-run-suspended",
		TimestampMS: 105,
	})
	require.NoError(t, err)

	decoded, err := DecodeFromRecordInput(record)
	require.NoError(t, err)
	got, ok := decoded.(*RunSuspendedEvent)
	require.True(t, ok)
	require.Equal(t, "suspension-1", got.SuspensionID)
	require.Equal(t, "v1", got.Version)
	require.Equal(t, 2, got.PendingCount)
	require.Equal(t, []tools.Ident{"svc.read", "svc.write"}, got.RequiredTools)
}

func TestDecodeFromRecordInput_RunCompletedRejectsFailedPayloadWithoutFailure(t *testing.T) {
	payload, err := json.Marshal(runCompletedPayload{
		Status: "failed",
		Phase:  run.PhaseFailed,
	})
	require.NoError(t, err)

	_, err = DecodeFromRecordInput(&runlog.ActivityInput{
		Type:        RunCompleted,
		RunID:       testRunID,
		AgentID:     agent.Ident("agent-1"),
		SessionID:   testSessionID,
		EventKey:    "evt-run-completed-failed",
		TimestampMS: time.Now().UnixMilli(),
		Payload:     rawjson.Message(payload),
	})
	require.ErrorContains(t, err, "failed run completion requires failure payload")
}

func TestDecodeFromRecordInput_RunCompletedRejectsCanceledPayloadWithoutReason(t *testing.T) {
	payload, err := json.Marshal(runCompletedPayload{
		Status:       "canceled",
		Phase:        run.PhaseCanceled,
		Cancellation: &run.Cancellation{},
	})
	require.NoError(t, err)

	_, err = DecodeFromRecordInput(&runlog.ActivityInput{
		Type:        RunCompleted,
		RunID:       testRunID,
		AgentID:     agent.Ident("agent-1"),
		SessionID:   testSessionID,
		EventKey:    "evt-run-completed-canceled",
		TimestampMS: time.Now().UnixMilli(),
		Payload:     rawjson.Message(payload),
	})
	require.ErrorContains(t, err, "canceled run completion requires cancellation reason")
}
