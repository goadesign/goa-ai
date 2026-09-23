package inmem

// Transcript reads select an immutable prefix while holding the same lock as
// appends and purge. Returned records are copies, so readers cannot edit history.

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const (
	// These per-page limits bound records and payload bytes copied while the
	// store lock is held. They do not limit the complete saved transcript.
	transcriptPageMaxRecords      = 512
	transcriptPageMaxPayloadBytes = 1 << 20
)

// ListRunTranscriptRecords returns transcript records through one committed
// position belonging to runID, excluding all later appends. Each page contains
// at most 512 records and an inclusive 1 MiB of payload bytes. A single record
// larger than that byte limit is unsupported and fails before it is copied.
func (s *Store) ListRunTranscriptRecords(ctx context.Context, runID, throughRecordID, afterRecordID string, limit int) (runlog.Page, error) {
	if err := ctx.Err(); err != nil {
		return runlog.Page{}, err
	}
	if throughRecordID == "" || limit <= 0 {
		return runlog.Page{}, storage.NewContractError(storage.ErrInvalidTranscriptPosition)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.runs[runID]; !ok {
		return runlog.Page{}, session.ErrRunNotFound
	}
	records := s.records[runID]
	end, after := -1, -1
	for i, record := range records {
		if record.ID == throughRecordID {
			end = i
		}
		if afterRecordID != "" && record.ID == afterRecordID &&
			(record.Type == transcript.RunLogMessagesSeeded || record.Type == transcript.RunLogMessagesAppended) {
			after = i
		}
	}
	if end < 0 || after > end || (afterRecordID != "" && after < 0) {
		return runlog.Page{}, storage.NewContractError(fmt.Errorf("%w: run %q", storage.ErrInvalidTranscriptPosition, runID))
	}
	limit = min(limit, transcriptPageMaxRecords)
	var page runlog.Page
	payloadBytes := 0
	for _, record := range records[after+1 : end+1] {
		if record.Type != transcript.RunLogMessagesSeeded && record.Type != transcript.RunLogMessagesAppended {
			continue
		}
		if len(page.Events) == limit {
			page.NextCursor = page.Events[len(page.Events)-1].ID
			break
		}
		size := len(record.Payload)
		if size > transcriptPageMaxPayloadBytes {
			return runlog.Page{}, storage.NewContractError(fmt.Errorf(
				"transcript record %q payload size %d exceeds supported page size %d bytes",
				record.ID, size, transcriptPageMaxPayloadBytes,
			))
		}
		if size > transcriptPageMaxPayloadBytes-payloadBytes {
			page.NextCursor = page.Events[len(page.Events)-1].ID
			break
		}
		page.Events = append(page.Events, cloneEvent(record))
		payloadBytes += size
	}
	return page, nil
}
