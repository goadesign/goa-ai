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

// runLogLister exposes only the reads required to select and expand a saved
// history. Callers need no mutation capability.
type runLogLister interface {
	transcriptPrefixStore
	ListRunRecords(ctx context.Context, runID string, cursor string, limit int) (runlog.Page, error)
}

// ReplayRunLogEvents replays canonical transcript seed and append records from
// an ordered run-log event slice.
func ReplayRunLogEvents(events []*runlog.Event) ([]*model.Message, bool, error) {
	var (
		messages []*model.Message
		found    bool
	)
	for _, event := range events {
		if event == nil || !isTranscriptRunLogType(event.Type) {
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

// BuildMessagesFromRunLog replays canonical transcript message events from the
// durable run log and returns the ordered provider-ready transcript.
func BuildMessagesFromRunLog(ctx context.Context, store runLogLister, runID string) ([]*model.Message, error) {
	if store == nil {
		return nil, fmt.Errorf("transcript: runlog store is required")
	}
	if runID == "" {
		return nil, fmt.Errorf("transcript: run id is required")
	}
	meta, err := store.LoadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	var cursor, endID string
	found := meta.SeedEndID != ""
	seen := make(map[string]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := store.ListRunRecords(ctx, runID, cursor, runlogReplayPageSize)
		if err != nil {
			return nil, fmt.Errorf("transcript: list runlog events for run %q: %w", runID, err)
		}
		for _, event := range page.Events {
			if event == nil || event.RunID != runID || event.ID == "" {
				return nil, prefixContractError("invalid run record while selecting history")
			}
			if _, repeated := seen[event.ID]; repeated {
				return nil, prefixContractError("repeated run record while selecting history")
			}
			seen[event.ID] = struct{}{}
			endID = event.ID
			found = found || isTranscriptRunLogType(event.Type)
		}
		if page.NextCursor == "" {
			break
		}
		if len(page.Events) == 0 || page.NextCursor == cursor || page.NextCursor != endID {
			return nil, prefixContractError("run page made no forward progress")
		}
		cursor = page.NextCursor
	}
	if !found {
		return nil, fmt.Errorf("transcript: runlog for run %q has no transcript message events", runID)
	}
	return BuildMessagesFromRunLogPrefix(ctx, store, runID, endID)
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

// isTranscriptRunLogType reports whether typ stores canonical transcript
// messages for replay.
func isTranscriptRunLogType(typ runlog.Type) bool {
	return typ == RunLogMessagesSeeded || typ == RunLogMessagesAppended
}
