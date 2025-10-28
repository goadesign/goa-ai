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
	// orchestratorsrvc is the service implementation for the orchestrator
	// service.
	orchestratorsrvc struct {
		runtime *RuntimeHarness
	}

	// sseSink adapts the runtime stream.Sink interface to the generated
	// orchestrator RunServerStream so runtime events are pushed to the
	// client.
	sseSink struct {
		stream orchestrator.RunServerStream
	}
)

// NewOrchestrator returns the orchestrator service implementation.
func NewOrchestrator() orchestrator.Service {
	h, err := NewRuntimeHarness(context.Background())
	if err != nil {
		panic(fmt.Sprintf("runtime harness: %v", err))
	}
	return &orchestratorsrvc{runtime: h}
}

func (s *orchestratorsrvc) Run(ctx context.Context, payload *orchestrator.AgentRunPayload, stream orchestrator.RunServerStream) error {
	// Convert Goa payload → apitypes → runtime
	rin, err := apitypes.ToRuntimeRunInput(payload.ConvertToRunInput())
	if err != nil {
		return fmt.Errorf("convert run input: %w", err)
	}

	// Attach a temporary stream subscriber that forwards runtime hook events to the client stream.
	bus := s.runtime.runtime.Bus
	subscription, err := streambridge.Register(bus, &sseSink{stream: stream})
	if err != nil {
		return err
	}
	defer subscription.Close()

	// Execute the run synchronously; events are forwarded as they occur.
	out, err := s.runtime.runtime.Run(ctx, rin)
	if err != nil {
		return err
	}

	// Send final assistant message as the closing chunk.
	finalText := out.Final.Content
	chunk := &orchestrator.AgentRunChunk{Type: "message", Message: &finalText}
	return stream.SendAndClose(ctx, chunk)
}

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
		return s.stream.Send(ctx, &orchestrator.AgentRunChunk{Type: "message", Message: &msg})
	case stream.PlannerThought:
		prefixed := "[planner] " + e.Note
		return s.stream.Send(ctx, &orchestrator.AgentRunChunk{Type: "message", Message: &prefixed})
	case stream.ToolEnd:
		chunk := &orchestrator.AgentRunChunk{Type: "tool_result", ToolResult: &orchestrator.AgentToolResultChunk{}}
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

func (s *sseSink) Close(context.Context) error { return nil }

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
