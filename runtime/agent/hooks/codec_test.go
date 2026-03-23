package hooks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	testRunID     = "run-1"
	testSessionID = "session-1"
)

func TestDecodeFromHookInput_ToolResultReceivedPreservesServerDataBytes(t *testing.T) {
	agentID := agent.Ident("agent-1")
	toolName := tools.Ident("svc.tools.lookup")
	toolCallID := "call-1"

	resultJSON := rawjson.Message([]byte(`{"summary":"ok"}`))
	serverData := rawjson.Message([]byte(`[{"kind":"example.topology","data":{"hello":"world","n":1}}]`))

	ev := NewToolResultReceivedEvent(
		testRunID,
		agentID,
		testSessionID,
		toolName,
		toolCallID,
		"",
		nil,
		resultJSON,
		serverData,
		"preview",
		nil,
		250*time.Millisecond,
		nil,
		nil,
		nil,
	)

	in, err := EncodeToHookInput(ev, "")
	require.NoError(t, err)

	decoded, err := DecodeFromHookInput(in)
	require.NoError(t, err)

	tr, ok := decoded.(*ToolResultReceivedEvent)
	require.True(t, ok)
	require.Equal(t, toolName, tr.ToolName)
	require.Equal(t, toolCallID, tr.ToolCallID)
	require.JSONEq(t, string(serverData), string(tr.ServerData))
}

func TestDecodeFromHookInput_PromptRenderedRoundTrip(t *testing.T) {
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

	in, err := EncodeToHookInput(ev, "turn-1")
	require.NoError(t, err)

	decoded, err := DecodeFromHookInput(in)
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

func TestEncodeToHookInputPreservesEventIdentity(t *testing.T) {
	agentID := agent.Ident("agent-1")

	ev := NewPromptRenderedEvent(
		testRunID,
		agentID,
		testSessionID,
		"example.agent.system",
		"v3",
		prompt.Scope{SessionID: testSessionID},
	)

	in, err := EncodeToHookInput(ev, "turn-1")
	require.NoError(t, err)
	require.Equal(t, ev.Timestamp(), in.TimestampMS)
	require.Equal(t, ev.EventKey(), in.EventKey)

	decoded, err := DecodeFromHookInput(in)
	require.NoError(t, err)
	require.Equal(t, ev.Timestamp(), decoded.Timestamp())
	require.Equal(t, ev.EventKey(), decoded.EventKey())
}

func TestDecodeFromHookInput_RunCompletedRejectsFailedPayloadWithoutFailure(t *testing.T) {
	payload, err := json.Marshal(runCompletedPayload{
		Status: "failed",
		Phase:  run.PhaseFailed,
	})
	require.NoError(t, err)

	_, err = DecodeFromHookInput(&ActivityInput{
		Type:        RunCompleted,
		RunID:       testRunID,
		AgentID:     agent.Ident("agent-1"),
		SessionID:   testSessionID,
		TimestampMS: time.Now().UnixMilli(),
		Payload:     rawjson.Message(payload),
	})
	require.ErrorContains(t, err, "failed run completion requires failure payload")
}

func TestDecodeFromHookInput_RunCompletedRejectsCanceledPayloadWithoutReason(t *testing.T) {
	payload, err := json.Marshal(runCompletedPayload{
		Status: "canceled",
		Phase:  run.PhaseCanceled,
		Cancellation: &run.Cancellation{},
	})
	require.NoError(t, err)

	_, err = DecodeFromHookInput(&ActivityInput{
		Type:        RunCompleted,
		RunID:       testRunID,
		AgentID:     agent.Ident("agent-1"),
		SessionID:   testSessionID,
		TimestampMS: time.Now().UnixMilli(),
		Payload:     rawjson.Message(payload),
	})
	require.ErrorContains(t, err, "canceled run completion requires cancellation reason")
}
