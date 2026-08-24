// Package bootstrap wires the goa-ai runtime and registers generated agents.
// Goa creates this file only when it does not already exist. The application
// owns all later edits.
package bootstrap

import (
	"context"

	chat "example.com/quickstart/gen/orchestrator/agents/chat"
	plannerchat "example.com/quickstart/internal/agents/chat/planner"
	toolsetchathelpers "example.com/quickstart/internal/agents/chat/toolsets/helpers"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
)

// Define flags for MCP endpoints (if any). Pass values via your cmd main.

// New constructs a minimal runtime and registers all agents for this service.
// Replace options (engine, stores, telemetry) as you adopt production wiring.
func New(ctx context.Context) (*agentsruntime.Runtime, func(), error) {
	rt := agentsruntime.New()
	cleanup := func() {}

	// Register agents with example planners. Replace with your own planner impls.
	{
		cfg := chat.ChatAgentConfig{Planner: plannerchat.New()}
		if err := chat.RegisterChatAgent(ctx, rt, cfg); err != nil {
			return nil, nil, err
		}
		// Register the application-owned example executors.
		if err := chat.RegisterUsedToolsets(ctx, rt,
			chat.WithHelpersExecutor(
				agentsruntime.ToolCallExecutorFunc(toolsetchathelpers.Execute),
			),
		); err != nil {
			return nil, nil, err
		}
	}

	return rt, cleanup, nil
}
