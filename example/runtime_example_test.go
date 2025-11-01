package assistantapi_test

import (
	"context"
	"testing"

	assistantapi "example.com/assistant"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agents/memory"
	"goa.design/goa-ai/runtime/agents/planner"
	agentsruntime "goa.design/goa-ai/runtime/agents/runtime"
	"goa.design/goa-ai/runtime/agents/stream"
)

func TestRuntimeHarnessExecutesChatWorkflow(t *testing.T) {
	ctx := context.Background()
	harness, err := assistantapi.NewRuntimeHarness(ctx)
	require.NoError(t, err)

	input := agentsruntime.RunInput{
		AgentID:   "orchestrator.chat",
		RunID:     "chat-run-1",
		SessionID: "session-42",
		Messages:  []planner.AgentMessage{{Role: "user", Content: "Need latest status"}},
	}

	out, err := harness.Run(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, out.Final)
	require.Contains(t, out.Final.Content, "Result for")

	events, err := harness.MemoryEvents(ctx, input.AgentID, input.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	// Ensure at least one assistant message was recorded in memory.
	hasAssistant := false
	for _, ev := range events {
		if ev.Type == memory.EventAssistantMessage {
			hasAssistant = true
			break
		}
	}
	require.True(t, hasAssistant, "expected at least one assistant message event")

	streamEvents := harness.StreamEvents()
	require.NotEmpty(t, streamEvents)
	assistantReplies := 0
	plannerThoughts := 0
	for _, evt := range streamEvents {
		switch evt.(type) {
		case stream.AssistantReply:
			assistantReplies++
		case stream.PlannerThought:
			plannerThoughts++
		}
	}
	require.GreaterOrEqual(t, assistantReplies, 2)
	require.GreaterOrEqual(t, plannerThoughts, 1)
}

func ExampleRuntimeHarness() {
	ctx := context.Background()
	harness, _ := assistantapi.NewRuntimeHarness(ctx)

	_, _ = harness.Run(ctx, agentsruntime.RunInput{
		AgentID: "orchestrator.chat",
		RunID:   "example-run",
		Messages: []planner.AgentMessage{
			{Role: "user", Content: "Summarize the MCP docs"},
		},
	})
	// Output:
}
