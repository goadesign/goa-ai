// Package provider implements the provider-side Pulse subscription loop for
// registry-routed tool execution. Providers receive tool calls from a toolset
// stream and publish results to per-call result streams.
package provider

import (
	"context"
	"encoding/json"
	"fmt"

	pulseclients "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	// Handler executes tool calls received from a toolset stream.
	// Implementations are responsible for decoding/encoding tool payload/result
	// using the compiled tool codecs for their toolset.
	Handler interface {
		HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error)
	}

	// Options configure the provider loop.
	Options struct {
		// SinkName identifies the Pulse sink used for subscribing.
		// When empty, defaults to "provider".
		SinkName string
		// ResultEventType is the Pulse entry type used for publishing results.
		// When empty, defaults to "result".
		ResultEventType string

		// Pong acknowledges health pings emitted by the registry gateway.
		// Providers must supply this to participate in health tracking.
		Pong func(ctx context.Context, pingID string) error
	}
)

// Serve subscribes to the toolset request stream and dispatches tool call
// messages to handler. It publishes tool results to per-call result streams.
func Serve(ctx context.Context, pulse pulseclients.Client, toolset string, handler Handler, opts Options) error {
	if pulse == nil {
		return fmt.Errorf("pulse client is required")
	}
	if toolset == "" {
		return fmt.Errorf("toolset is required")
	}
	if handler == nil {
		return fmt.Errorf("handler is required")
	}
	sinkName := opts.SinkName
	if sinkName == "" {
		sinkName = "provider"
	}
	resultEventType := opts.ResultEventType
	if resultEventType == "" {
		resultEventType = "result"
	}
	if opts.Pong == nil {
		return fmt.Errorf("pong handler is required")
	}

	streamID := toolregistry.ToolsetStreamID(toolset)
	stream, err := pulse.Stream(streamID)
	if err != nil {
		return fmt.Errorf("open toolset stream %q: %w", streamID, err)
	}
	sink, err := stream.NewSink(ctx, sinkName)
	if err != nil {
		return fmt.Errorf("create sink %q for toolset stream %q: %w", sinkName, streamID, err)
	}
	defer sink.Close(ctx)

	events := sink.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("toolset stream subscription closed")
			}
			var msg toolregistry.ToolCallMessage
			if err := json.Unmarshal(ev.Payload, &msg); err != nil {
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return fmt.Errorf("ack malformed toolset event: %w", ackErr)
				}
				continue
			}
			switch msg.Type {
			case toolregistry.MessageTypePing:
				if msg.PingID != "" {
					if err := opts.Pong(ctx, msg.PingID); err != nil {
						return fmt.Errorf("pong: %w", err)
					}
				}
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return fmt.Errorf("ack ping toolset event: %w", ackErr)
				}
				continue
			case toolregistry.MessageTypeCall:
			default:
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return fmt.Errorf("ack unknown toolset event: %w", ackErr)
				}
				continue
			}
			if msg.ToolUseID == "" {
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return fmt.Errorf("ack tool call missing tool_use_id: %w", ackErr)
				}
				continue
			}

			res, err := handler.HandleToolCall(ctx, msg)
			if err != nil {
				res = toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "execution_failed", err.Error())
			}

			resultStreamID := toolregistry.ResultStreamID(msg.ToolUseID)
			resultStream, streamErr := pulse.Stream(resultStreamID)
			if streamErr != nil {
				return fmt.Errorf("open result stream %q: %w", resultStreamID, streamErr)
			}
			payload, marshalErr := json.Marshal(res)
			if marshalErr != nil {
				return fmt.Errorf("marshal tool result: %w", marshalErr)
			}
			if _, addErr := resultStream.Add(ctx, resultEventType, payload); addErr != nil {
				return fmt.Errorf("publish tool result to %q: %w", resultStreamID, addErr)
			}
			if ackErr := sink.Ack(ctx, ev); ackErr != nil {
				return fmt.Errorf("ack tool call event: %w", ackErr)
			}
		}
	}
}
