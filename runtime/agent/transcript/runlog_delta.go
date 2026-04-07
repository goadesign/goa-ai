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

const (
	// RunLogMessagesSeeded is the durable run-log record type for canonical
	// transcript messages that seeded a run at start. Seeded transcript messages
	// describe planner input that already existed before the run began, so they
	// must never be fanned out as newly committed assistant turns.
	RunLogMessagesSeeded runlog.Type = "transcript_messages_seeded"

	// RunLogMessagesAppended is the durable run-log record type for canonical
	// transcript messages appended during a run. Appended transcript messages are
	// eligible for committed assistant-turn fanout to session-aware consumers.
	RunLogMessagesAppended runlog.Type = "transcript_messages_appended"
)

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
