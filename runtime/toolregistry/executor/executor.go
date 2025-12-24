// Package executor provides registry-backed tool execution. It routes tool
// invocations through the registry gateway and awaits results on Pulse streams.
package executor

import (
	"context"
	"encoding/json"
	"fmt"

	pulseclients "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/planner"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	// Client initiates tool calls through a registry gateway.
	Client interface {
		CallTool(ctx context.Context, toolset string, tool tools.Ident, payload []byte, meta toolregistry.ToolCallMeta) (toolUseID string, resultStreamID string, err error)
	}

	// SpecLookup resolves tool specifications for decoding results and artifacts.
	SpecLookup interface {
		Spec(name tools.Ident) (*tools.ToolSpec, bool)
	}

	Executor struct {
		client Client
		pulse  pulseclients.Client
		specs  SpecLookup

		sinkName       string
		resultEventKey string
	}

	Option func(*Executor)
)

func WithSinkName(name string) Option {
	return func(e *Executor) {
		e.sinkName = name
	}
}

func WithResultEventKey(key string) Option {
	return func(e *Executor) {
		e.resultEventKey = key
	}
}

func New(client Client, pulse pulseclients.Client, specs SpecLookup, opts ...Option) *Executor {
	e := &Executor{
		client:         client,
		pulse:          pulse,
		specs:          specs,
		sinkName:       "agent",
		resultEventKey: "result",
	}
	for _, o := range opts {
		if o != nil {
			o(e)
		}
	}
	return e
}

func (e *Executor) Execute(ctx context.Context, meta *agentsruntime.ToolCallMeta, call *planner.ToolRequest) (*planner.ToolResult, error) {
	if call == nil {
		return &planner.ToolResult{Error: planner.NewToolError("tool request is nil")}, nil
	}
	if meta == nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError("tool call meta is nil")}, nil
	}
	if e.client == nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError("registry client is nil")}, nil
	}
	if e.pulse == nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError("pulse client is nil")}, nil
	}
	if e.specs == nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError("tool specs lookup is nil")}, nil
	}

	spec, ok := e.specs.Spec(call.Name)
	if !ok {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError(fmt.Sprintf("unknown tool %q", call.Name))}, nil
	}
	toolsetID := spec.Toolset
	if toolsetID == "" {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError(fmt.Sprintf("tool %q missing toolset routing id", call.Name))}, nil
	}

	tmeta := toolregistry.ToolCallMeta{
		RunID:            meta.RunID,
		SessionID:        meta.SessionID,
		TurnID:           meta.TurnID,
		ToolCallID:       meta.ToolCallID,
		ParentToolCallID: meta.ParentToolCallID,
	}
	toolUseID, resultStreamID, err := e.client.CallTool(ctx, toolsetID, call.Name, call.Payload, tmeta)
	if err != nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err), ToolCallID: meta.ToolCallID}, nil
	}

	stream, err := e.pulse.Stream(resultStreamID)
	if err != nil {
		return nil, fmt.Errorf("open tool result stream %q: %w", resultStreamID, err)
	}
	sink, err := stream.NewSink(ctx, e.sinkName)
	if err != nil {
		return nil, fmt.Errorf("create sink %q for tool result stream %q: %w", e.sinkName, resultStreamID, err)
	}
	defer sink.Close(ctx)

	events := sink.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil, fmt.Errorf("tool result stream subscription closed")
			}
			if ev.EventName != e.resultEventKey {
				if err := sink.Ack(ctx, ev); err != nil {
					return nil, fmt.Errorf("ack tool result stream event: %w", err)
				}
				continue
			}

			var msg toolregistry.ToolResultMessage
			if err := json.Unmarshal(ev.Payload, &msg); err != nil {
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return nil, fmt.Errorf("ack malformed tool result message: %w", ackErr)
				}
				continue
			}
			if msg.ToolUseID != toolUseID {
				if err := sink.Ack(ctx, ev); err != nil {
					return nil, fmt.Errorf("ack unrelated tool result message: %w", err)
				}
				continue
			}
			if err := sink.Ack(ctx, ev); err != nil {
				return nil, fmt.Errorf("ack tool result message: %w", err)
			}
			if destroyErr := stream.Destroy(ctx); destroyErr != nil {
				return nil, fmt.Errorf("destroy tool result stream %q: %w", resultStreamID, destroyErr)
			}
			return e.decodeToolResult(spec, call.Name, meta.ToolCallID, msg), nil
		}
	}
}

func (e *Executor) decodeToolResult(spec *tools.ToolSpec, tool tools.Ident, toolCallID string, msg toolregistry.ToolResultMessage) *planner.ToolResult {
	out := &planner.ToolResult{
		Name:       tool,
		ToolCallID: toolCallID,
	}
	if msg.Error != nil {
		out.Error = planner.NewToolError(msg.Error.Message)
		return out
	}
	if spec.Result.Codec.FromJSON != nil {
		res, err := spec.Result.Codec.FromJSON(msg.Result)
		if err != nil {
			out.Error = planner.ToolErrorFromError(err)
			return out
		}
		out.Result = res
	}
	if spec.Sidecar != nil && spec.Sidecar.Codec.FromJSON != nil && len(msg.Artifacts) > 0 {
		arts := make([]*planner.Artifact, 0, len(msg.Artifacts))
		for _, a := range msg.Artifacts {
			data, err := spec.Sidecar.Codec.FromJSON(a.Data)
			if err != nil {
				out.Error = planner.ToolErrorFromError(err)
				return out
			}
			arts = append(arts, &planner.Artifact{
				Kind:       a.Kind,
				Data:       data,
				SourceTool: tool,
			})
		}
		out.Artifacts = arts
	}
	return out
}


