package hooks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDecodeFromHookInput_ToolResultReceivedPreservesServerDataBytes(t *testing.T) {
	runID := "run-1"
	agentID := agent.Ident("agent-1")
	sessionID := "session-1"
	toolName := tools.Ident("atlas.read.get_topology")
	toolCallID := "call-1"

	resultJSON := json.RawMessage(`{"summary":"ok"}`)
	serverData := json.RawMessage(`[{"kind":"atlas.topology","data":{"hello":"world","n":1}}]`)

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
