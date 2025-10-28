package assistantapi

import (
	"context"
	"fmt"

	"goa.design/goa-ai/agents/apitypes"
	"goa.design/goa-ai/agents/runtime/stream"
	streambridge "goa.design/goa-ai/agents/runtime/stream/bridge"
	"goa.design/goa-ai/agents/runtime/toolerrors"

	"example.com/assistant/gen/orchestrator"
)

type (
	// orchestratorsrvc is the service implementation for the orchestrator service.
	// It wraps a RuntimeHarness to execute agent workflows and stream events to
	// clients over Server-Sent Events.
	orchestratorsrvc struct {
		harness *RuntimeHarness
	}

	// sseSink adapts the runtime stream.Sink interface to the generated
	// orchestrator RunServerStream so runtime events are pushed to the client
	// as Server-Sent Events. It converts stream events into Goa-generated chunk
	// types for wire transmission.
	sseSink struct {
		stream orchestrator.RunServerStream
	}
)

// NewOrchestrator returns the orchestrator service implementation. It constructs
// a RuntimeHarness with in-memory stores and an example MCP caller, then wraps
// it in the service implementation.
//
// Panics if the runtime harness construction fails (e.g., agent registration
// errors). This is acceptable for example code where startup failures should
// be immediately visible.
func NewOrchestrator() orchestrator.Service {
	h, err := NewRuntimeHarness(context.Background())
	if err != nil {
		panic(fmt.Sprintf("runtime harness: %v", err))
	}
	return &orchestratorsrvc{harness: h}
}

// Run executes an agent workflow and streams events to the client over SSE.
// It converts the Goa payload to runtime types, attaches a stream subscriber
// to forward events, executes the workflow synchronously, and returns the
// final assistant message.
//
// The stream subscriber is automatically unregistered when Run returns,
// ensuring no event leaks even if the workflow fails.
func (s *orchestratorsrvc) Run(
	ctx context.Context,
	payload *orchestrator.AgentRunPayload,
	stream orchestrator.RunServerStream,
) error {
	// Convert Goa payload → apitypes → runtime
	rin, err := apitypes.ToRuntimeRunInput(payload.ConvertToRunInput())
	if err != nil {
		return fmt.Errorf("convert run input: %w", err)
	}

	// Attach a temporary stream subscriber that forwards runtime hook events to
	// the client stream.
	bus := s.harness.runtime.Bus
	subscription, err := streambridge.Register(bus, &sseSink{stream: stream})
	if err != nil {
		return err
	}
	var closeErr error
	defer func() {
		if err := subscription.Close(); err != nil {
			closeErr = fmt.Errorf("close subscription: %w", err)
		}
	}()

	// Execute the run synchronously; events are forwarded as they occur.
	out, err := s.harness.runtime.Run(ctx, rin)
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}

	// Send final assistant message as the closing chunk.
	finalText := out.Final.Content
	chunk := &orchestrator.AgentRunChunk{Type: "message", Message: &finalText}
	return stream.SendAndClose(ctx, chunk)
}

// Send converts a runtime stream event into a Goa-generated chunk type and sends
// it to the SSE stream. It handles ToolStart, ToolEnd, AssistantReply, and
// PlannerThought events. Unknown event types are silently ignored.
func (s *sseSink) Send(ctx context.Context, evt stream.Event) error {
	switch e := evt.(type) {
	case stream.ToolStart:
		chunk := &orchestrator.AgentRunChunk{
			Type: "tool_call",
			ToolCall: &orchestrator.AgentToolCallChunk{
				ID:      e.Data.ToolCallID,
				Name:    e.Data.ToolName,
				Payload: e.Data.Payload,
			},
		}
		return s.stream.Send(ctx, chunk)
	case stream.AssistantReply:
		msg := e.Text
		chunk := &orchestrator.AgentRunChunk{Type: "message", Message: &msg}
		return s.stream.Send(ctx, chunk)
	case stream.PlannerThought:
		prefixed := "[planner] " + e.Note
		chunk := &orchestrator.AgentRunChunk{Type: "message", Message: &prefixed}
		return s.stream.Send(ctx, chunk)
	case stream.ToolEnd:
		chunk := &orchestrator.AgentRunChunk{
			Type:       "tool_result",
			ToolResult: &orchestrator.AgentToolResultChunk{},
		}
		chunk.ToolResult.ID = e.Data.ToolCallID
		if e.Data.Result != nil {
			chunk.ToolResult.Result = e.Data.Result
		}
		if e.Data.Error != nil {
			chunk.ToolResult.Error = toAgentToolError(e.Data.Error)
		}
		return s.stream.Send(ctx, chunk)
	}
	return nil
}

// Close is a no-op since the SSE stream is managed by the Goa framework and
// closed automatically when the HTTP request completes.
func (s *sseSink) Close(context.Context) error {
	return nil
}

// toAgentToolError recursively converts a toolerrors.ToolError into the
// Goa-generated AgentToolError type for wire transmission. Preserves the
// error chain by recursively converting the Cause field.
func toAgentToolError(err *toolerrors.ToolError) *orchestrator.AgentToolError {
	if err == nil {
		return nil
	}
	msg := err.Message
	out := &orchestrator.AgentToolError{Message: &msg}
	if err.Cause != nil {
		out.Cause = toAgentToolError(err.Cause)
	}
	return out
}
