// Package lifecycle selects whether a saved stop prevents ordinary work at first start.
// This constructor gives every store the same cancellation record while the
// store keeps responsibility for choosing the reason in its transaction.
package lifecycle

import (
	"encoding/json"
	"errors"

	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
)

const cancellationEventKey = "runtime/cancellation-intent"

// StartCancellation builds a cancellation record from the accepted start's
// identity and time. The owner supplies the reason selected in the same
// transaction; callers receive bytes that exact retries can reuse unchanged.
func StartCancellation(started *runlog.Event, reason string) (*runlog.Event, error) {
	if reason == "" {
		return nil, errors.New("cancellation reason is required")
	}
	payload, err := json.Marshal(cancellationPayload{Reason: reason})
	if err != nil {
		return nil, err
	}
	return &runlog.Event{
		EventKey: cancellationEventKey,
		RunID:    started.RunID, Subject: started.Subject, SessionID: started.SessionID,
		TurnID: started.TurnID, Timestamp: started.Timestamp,
		Type: storage.CancellationRecordType, Payload: payload,
	}, nil
}
