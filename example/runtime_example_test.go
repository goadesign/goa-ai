package assistantapi_test

import (
	"context"
	"testing"

	assistantapi "example.com/assistant"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/agents/runtime/memory"
	"goa.design/goa-ai/agents/runtime/planner"
	agentsruntime "goa.design/goa-ai/agents/runtime/runtime"
	"goa.design/goa-ai/agents/runtime/stream"
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
	require.Equal(t, memory.EventAssistantMessage, events[len(events)-1].Type)

	streamEvents := harness.StreamEvents()
	require.NotEmpty(t, streamEvents)
	assistantReplies := 0
	plannerThoughts := 0
	for _, evt := range streamEvents {
		switch evt.Type {
		case stream.EventAssistantReply:
			assistantReplies++
		case stream.EventPlannerThought:
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
