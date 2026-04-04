// Package transcript stores and replays canonical provider-ready transcript
// deltas from the durable run log.
package transcript

import (
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
)

// RunLogMessagesAppended is the durable run-log record type for canonical
// transcript deltas. It is not a hook event and is never published to the hook
// bus or stream subscribers.
const RunLogMessagesAppended runlog.Type = "transcript_messages_appended"

// EncodeRunLogDelta encodes canonical transcript messages for durable run-log
// storage using model.Message JSON as the single payload format.
func EncodeRunLogDelta(messages []*model.Message) (rawjson.Message, error) {
	b, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("transcript: marshal runlog delta: %w", err)
	}
	return rawjson.Message(b), nil
}

// DecodeRunLogDelta decodes one durable transcript delta payload back into
// canonical provider-ready messages.
func DecodeRunLogDelta(payload rawjson.Message) ([]*model.Message, error) {
	var messages []*model.Message
	if err := json.Unmarshal(payload, &messages); err != nil {
		return nil, fmt.Errorf("transcript: unmarshal runlog delta: %w", err)
	}
	return messages, nil
}
