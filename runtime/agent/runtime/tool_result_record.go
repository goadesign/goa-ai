package runtime

// A materialized result event owns its canonical result bytes and rendered
// preview. Restoring or appending that event must not render the original input
// again. The existing hook correlation contract owns its execution identity.

import (
	"bytes"
	"fmt"
	"reflect"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// decodeToolResultRecord checks the stored event's kind, immutable call/result
// provenance and byte count. Result codecs are validated separately at the
// execution or checkpoint boundary before transcript projection.
func decodeToolResultRecord(input *RecordActivityInput, call ToolCall, callRunID, resultRunID string) (*hooks.ToolResultReceivedEvent, error) {
	decoded, err := hooks.DecodeFromRecordInput(input)
	if err != nil {
		return nil, err
	}
	event, ok := decoded.(*hooks.ToolResultReceivedEvent)
	if !ok {
		return nil, fmt.Errorf("saved result record for call %q is not a tool result", call.ToolCallID)
	}
	scheduled := newToolCallScheduledEvent(
		callRunID, call.AgentID, call.SessionID, call, "", call.ParentToolCallID, 0,
	)
	if err := hooks.ValidateToolResultCorrelation(scheduled, event); err != nil {
		return nil, err
	}
	if event.RunID() != resultRunID {
		return nil, fmt.Errorf("saved result run %q does not match result run %q for call %q", event.RunID(), resultRunID, call.ToolCallID)
	}
	if event.ResultBytes != len(event.ResultJSON) {
		return nil, fmt.Errorf("saved result byte count does not match result for call %q", call.ToolCallID)
	}
	return event, nil
}

// validateCheckpointResultRecord checks the materialized event before it can
// replace input-dependent rendering. Both result representations must satisfy
// the selected current result contract and describe the same typed result.
func validateCheckpointResultRecord(record checkpointToolRecord, checkpoint *workflowCheckpoint, definition AgentDefinition) error {
	event, err := decodeToolResultRecord(record.ResultRecord, record.Call, record.CallRunID, record.ResultRunID)
	if err != nil {
		return fmt.Errorf("decode saved result record: %w", err)
	}
	if event.AgentID() != checkpoint.AgentID || event.SessionID() != checkpoint.SessionID ||
		event.ParentToolCallID != checkpoint.Context.ParentToolCallID {
		return fmt.Errorf("saved result record owner does not match checkpoint for call %q", record.Call.ToolCallID)
	}
	saved, err := decodeCheckpointToolEvent(&api.ToolEvent{
		Name: event.ToolName, ToolCallID: event.ToolCallID, Result: event.ResultJSON,
		ServerData: event.ServerData, Blocks: event.Blocks, Bounds: event.Bounds, Failure: event.Failure,
	}, record.Call, definition.spec)
	if err != nil {
		return err
	}
	result, err := decodeCheckpointToolEvent(record.Result, record.Call, definition.spec)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(saved.Result, result.Result) ||
		!reflect.DeepEqual(saved.Failure, result.Failure) ||
		!reflect.DeepEqual(saved.Bounds, result.Bounds) ||
		((len(saved.Blocks) != 0 || len(result.Blocks) != 0) && !reflect.DeepEqual(saved.Blocks, result.Blocks)) ||
		!bytes.Equal(saved.ServerData, result.ServerData) {
		return fmt.Errorf("saved result record disagrees with batch result for call %q", record.Call.ToolCallID)
	}
	if len(record.ResultJSON) > 0 && !bytes.Equal(record.ResultJSON, event.ResultJSON) {
		return fmt.Errorf("saved result record disagrees with materialized bytes for call %q", record.Call.ToolCallID)
	}
	return nil
}

// toolResultRecordPart reads the accepted event and returns its correlated model
// result, including validated media and failure status. An empty saved preview
// is complete; the runtime never renders the original input again.
func toolResultRecordPart(record stepToolRecord) (model.ToolResultPart, error) {
	event, err := decodeToolResultRecord(record.resultRecord, record.call, record.callRunID, record.resultRunID)
	if err != nil {
		return model.ToolResultPart{}, err
	}
	errorMessage := ""
	if event.Failure != nil {
		errorMessage = event.Failure.Error.Error()
	}
	semantic, err := transcript.ProjectToolResultContent(event.ResultJSON, event.Bounds, event.ResultPreview, errorMessage)
	if err != nil {
		return model.ToolResultPart{}, err
	}
	return model.ToolResultPart{
		ToolUseID: transcriptToolCallID(record.call),
		Content:   semantic,
		Blocks:    event.Blocks.Clone(),
		IsError:   event.Failure != nil,
	}, nil
}
