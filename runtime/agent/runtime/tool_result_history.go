package runtime

// Completed tool results have already passed generated validation.
// Saved history keeps their accepted JSON and metadata intact across releases;
// pending materialization and new results still use current generated codecs.

import (
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/toolserverdata"
)

// historicalToolResult keeps accepted bytes distinct from generated Go values.
// Only restored completed history uses this private type; executors never do.
type historicalToolResult struct {
	result rawjson.Message
}

// validateHistoricalToolEvent checks the saved call identity, JSON envelopes,
// media, failure, and continuation metadata without applying a newer schema.
func validateHistoricalToolEvent(event *api.ToolEvent, call ToolCall) error {
	if event == nil {
		return errors.New("run suspension contains nil tool event")
	}
	if event.Name != call.Name || event.ToolCallID != call.ToolCallID || call.Name == "" || call.ToolCallID == "" {
		return errors.New("suspended tool result does not match its saved call")
	}
	if call.Registry != nil {
		if _, err := registrycontract.RegistrationToken(call.Registry); err != nil {
			return err
		}
	}
	if err := event.Blocks.Validate(); err != nil {
		return fmt.Errorf("invalid stored tool content: %w", err)
	}
	if event.Failure != nil {
		_, err := validatePersistedToolResult(nil, call, event.Result, event.ServerData, event.Blocks, event.Bounds, event.Failure)
		return err
	}
	if len(event.Result) > 0 && !json.Valid(event.Result) {
		return errors.New("invalid stored tool result JSON")
	}
	if _, err := toolserverdata.Canonicalize(event.ServerData, validateHistoricalServerDataItem); err != nil {
		return fmt.Errorf("invalid stored server data: %w", err)
	}
	if event.Bounds != nil {
		return validateToolBoundsMetadata(call, event.Bounds)
	}
	return nil
}

// restoreHistoricalToolEvent gives bookkeeping the accepted metadata and keeps
// result bytes private so later checkpoints never invoke a newer result codec.
func restoreHistoricalToolEvent(event *api.ToolEvent, call ToolCall) (*planner.ToolResult, error) {
	if err := validateHistoricalToolEvent(event, call); err != nil {
		return nil, err
	}
	return &planner.ToolResult{
		Name: event.Name, ToolCallID: event.ToolCallID,
		Result:     historicalToolResult{result: append(rawjson.Message(nil), event.Result...)},
		ServerData: append(rawjson.Message(nil), event.ServerData...), Blocks: event.Blocks.Clone(),
		Bounds: event.Bounds, Failure: planner.CloneToolFailure(event.Failure),
		Telemetry: event.Telemetry, ChildrenCount: event.ChildrenCount, RunLink: event.RunLink,
	}, nil
}

// validateHistoricalServerDataItem checks the common envelope while retaining
// a previously accepted kind-specific payload without a newer generated codec.
func validateHistoricalServerDataItem(kind, audience string, data rawjson.Message) (string, rawjson.Message, error) {
	if kind == "" || len(data) == 0 || !json.Valid(data) {
		return "", nil, errors.New("server data item requires kind and valid data")
	}
	switch audience {
	case "timeline", "internal", "evidence":
		return audience, data, nil
	default:
		return "", nil, fmt.Errorf("unknown server data audience %q", audience)
	}
}
