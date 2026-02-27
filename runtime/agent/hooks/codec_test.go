package hooks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDecodeFromHookInput_ToolResultReceivedPreservesServerDataBytes(t *testing.T) {
	runID := "run-1"
	agentID := agent.Ident("agent-1")
	sessionID := "session-1"
	toolName := tools.Ident("svc.tools.lookup")
	toolCallID := "call-1"

	resultJSON := rawjson.RawJSON([]byte(`{"summary":"ok"}`))
	serverData := rawjson.RawJSON([]byte(`[{"kind":"example.topology","data":{"hello":"world","n":1}}]`))

	ev := NewToolResultReceivedEvent(
		runID,
		agentID,
		sessionID,
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
	runID := "run-1"
	agentID := agent.Ident("agent-1")
	sessionID := "session-1"

	ev := NewPromptRenderedEvent(
		runID,
		agentID,
		sessionID,
		"example.agent.system",
		"v3",
		prompt.Scope{
			SessionID: sessionID,
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
	require.Equal(t, runID, got.RunID())
	require.Equal(t, string(agentID), got.AgentID())
	require.Equal(t, sessionID, got.SessionID())
	require.Equal(t, "turn-1", got.TurnID())
	require.Equal(t, prompt.Ident("example.agent.system"), got.PromptID)
	require.Equal(t, "v3", got.Version)
	require.Equal(t, sessionID, got.Scope.SessionID)
	require.Equal(t, "acme", got.Scope.Labels["account"])
	require.Equal(t, "west", got.Scope.Labels["region"])
}
