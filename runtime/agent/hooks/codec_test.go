package hooks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDecodeFromHookInput_ToolResultReceivedPreservesArtifactBytes(t *testing.T) {
	runID := "run-1"
	agentID := agent.Ident("agent-1")
	sessionID := "session-1"
	toolName := tools.Ident("atlas.read.get_topology")
	toolCallID := "call-1"

	artifactJSON := json.RawMessage(`{"hello":"world","n":1}`)
	resultJSON := json.RawMessage(`{"summary":"ok"}`)

	ev := NewToolResultReceivedEvent(
		runID,
		agentID,
		sessionID,
		toolName,
		toolCallID,
		"",
		nil,
		resultJSON,
		nil,
		"preview",
		nil,
		[]*planner.Artifact{
			{
				Kind:       "atlas.topology",
				Data:       artifactJSON,
				SourceTool: toolName,
			},
		},
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
	require.Len(t, tr.Artifacts, 1)

	raw, ok := tr.Artifacts[0].Data.(json.RawMessage)
	require.True(t, ok, "artifact data must remain json.RawMessage after hook decode")
	require.JSONEq(t, string(artifactJSON), string(raw))
}
