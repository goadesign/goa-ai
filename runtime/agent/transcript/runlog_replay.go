// Package transcript rebuilds provider-ready transcripts from durable runtime
// events. Runlog replay is the canonical generic recovery path once runtimes
// append exact transcript deltas as run-log records.
package transcript

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
)

const runlogReplayPageSize = 512

// ReplayRunLogEvents replays canonical transcript delta records from an ordered
// run-log event slice.
func ReplayRunLogEvents(events []*runlog.Event) ([]*model.Message, bool, error) {
	var (
		messages []*model.Message
		found    bool
	)
	for _, event := range events {
		if event == nil || event.Type != RunLogMessagesAppended {
			continue
		}
		delta, err := decodeTranscriptMessagesDelta(event)
		if err != nil {
			return nil, false, err
		}
		messages = append(messages, delta...)
		found = true
	}
	return messages, found, nil
}

// BuildMessagesFromRunLog replays canonical transcript delta events from the
// durable run log and returns the ordered provider-ready transcript.
func BuildMessagesFromRunLog(ctx context.Context, store runlog.Store, runID string) ([]*model.Message, error) {
	if store == nil {
		return nil, fmt.Errorf("transcript: runlog store is required")
	}
	if runID == "" {
		return nil, fmt.Errorf("transcript: run id is required")
	}
	var (
		cursor   string
		messages []*model.Message
		found    bool
	)
	for {
		page, err := store.List(ctx, runID, cursor, runlogReplayPageSize)
		if err != nil {
			return nil, fmt.Errorf("transcript: list runlog events for run %q: %w", runID, err)
		}
		replayed, pageFound, err := ReplayRunLogEvents(page.Events)
		if err != nil {
			return nil, err
		}
		messages = append(messages, replayed...)
		found = found || pageFound
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !found {
		return nil, fmt.Errorf("transcript: runlog for run %q has no transcript delta events", runID)
	}
	return messages, nil
}

// decodeTranscriptMessagesDelta decodes a single durable runlog event into the
// canonical transcript messages it appended.
func decodeTranscriptMessagesDelta(event *runlog.Event) ([]*model.Message, error) {
	if event == nil {
		return nil, fmt.Errorf("transcript: nil runlog event")
	}
	delta, err := DecodeRunLogDelta(event.Payload)
	if err != nil {
		return nil, fmt.Errorf("transcript: decode runlog event %q for run %q: %w", event.EventKey, event.RunID, err)
	}
	return delta, nil
}
