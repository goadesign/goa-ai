package transcript

// Prefix replay reconstructs the exact saved messages selected by a workflow
// command. The store validates positions and applies the end bound before reads.

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
)

type transcriptPrefixLister interface {
	ListRunTranscriptRecords(ctx context.Context, runID, throughRecordID, afterRecordID string, limit int) (runlog.Page, error)
}

// BuildMessagesFromRunLogPrefix reads only the selected run's transcript records
// through endID. A RunStarted position with no messages yields an empty history.
func BuildMessagesFromRunLogPrefix(ctx context.Context, store transcriptPrefixLister, runID, endID string) ([]*model.Message, error) {
	if store == nil || runID == "" || endID == "" {
		return nil, storage.NewContractError(fmt.Errorf("transcript: store, run id and end record id are required"))
	}
	var messages []*model.Message
	cursor := ""
	seen := make(map[string]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := store.ListRunTranscriptRecords(ctx, runID, endID, cursor, runlogReplayPageSize)
		if err != nil {
			return nil, fmt.Errorf("transcript: read run %q through %q: %w", runID, endID, err)
		}
		for _, record := range page.Events {
			if record == nil || record.RunID != runID || record.ID == "" || !isTranscriptRunLogType(record.Type) {
				return nil, storage.NewContractError(fmt.Errorf("transcript: store returned an invalid record for run %q", runID))
			}
			if _, duplicate := seen[record.ID]; duplicate {
				return nil, storage.NewContractError(fmt.Errorf("transcript: store repeated record %q", record.ID))
			}
			seen[record.ID] = struct{}{}
			delta, err := decodeTranscriptMessagesDelta(record)
			if err != nil {
				return nil, storage.NewContractError(err)
			}
			messages = append(messages, delta...)
		}
		if page.NextCursor == "" {
			return messages, nil
		}
		if len(page.Events) == 0 || page.NextCursor == cursor || page.NextCursor != page.Events[len(page.Events)-1].ID {
			return nil, storage.NewContractError(fmt.Errorf("transcript: store returned a page without forward progress"))
		}
		cursor = page.NextCursor
	}
}
