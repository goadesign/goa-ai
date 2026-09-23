package transcript

// Prefix replay reconstructs the exact saved messages selected by a workflow
// command. The store validates positions and applies the end bound before reads.

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	transcriptPrefixStore interface {
		LoadRun(context.Context, string) (session.RunMeta, error)
		LoadRunSeed(ctx context.Context, runID, endID string) (storage.RunSeed, error)
		ListRunSeedRecords(ctx context.Context, runID, endID, afterID string, limit int) (storage.SeedPage, error)
		ListRunTranscriptRecords(ctx context.Context, runID, throughRecordID, afterRecordID string, limit int) (runlog.Page, error)
	}

	// prefixFrame retains only the current bounded pages while its source is
	// visited. Completed sources append to one output, never an ancestor copy.
	prefixFrame struct {
		ref        storage.HistoryPrefix
		meta       session.RunMeta
		seed       storage.RunSeed
		seedPage   storage.SeedPage
		seedIndex  int
		seedLoaded bool
		seedDone   bool
		seedAfter  string
		sourceSeen bool
		seedSeen   map[string]struct{}
		logPage    runlog.Page
		logIndex   int
		logSeen    map[string]struct{}
	}
)

// ErrInvalidHistory identifies invalid saved message content or a saved history
// reference that cannot belong to the selected conversation. Store failures,
// including invalid response pages, do not establish that history is invalid.
var ErrInvalidHistory = errors.New("invalid saved transcript history")

// BuildMessagesFromRunLogPrefix reads only the selected run's transcript records
// through endID. A RunStarted position with no messages yields an empty history.
func BuildMessagesFromRunLogPrefix(ctx context.Context, store transcriptPrefixStore, runID, endID string) ([]*model.Message, error) {
	if store == nil || runID == "" || endID == "" {
		return nil, storage.NewContractError(fmt.Errorf("transcript: store, run id and end record id are required"))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := loadPrefixFrame(ctx, store, storage.HistoryPrefix{RunID: runID, EndID: endID})
	if err != nil {
		return nil, err
	}
	stack := []*prefixFrame{root}
	active := map[string]struct{}{runID: {}}
	var messages []*model.Message
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame := stack[len(stack)-1]
		if !frame.seedDone {
			record, err := frame.nextSeedRecord(ctx, store)
			if err != nil {
				return nil, err
			}
			if record == nil {
				continue
			}
			if record.Prefix != nil {
				if frame.sourceSeen || !reflect.DeepEqual(record.Prefix, frame.seed.Source) {
					return nil, invalidHistoryError(errors.New("seed reference differs from its declared source"))
				}
				frame.sourceSeen = true
				ref := *record.Prefix
				ref.ExcludeSystem = ref.ExcludeSystem || frame.ref.ExcludeSystem
				ref.ExcludeReasoning = ref.ExcludeReasoning || frame.ref.ExcludeReasoning
				if _, cycle := active[ref.RunID]; cycle {
					return nil, invalidHistoryError(fmt.Errorf("history reference cycle at run %q", ref.RunID))
				}
				child, err := loadPrefixFrame(ctx, store, ref)
				if err != nil {
					return nil, err
				}
				if root.meta.SessionID == "" || child.meta.SessionID != root.meta.SessionID ||
					child.meta.AgentID != root.meta.AgentID {
					return nil, invalidHistoryError(storage.ErrRunRecordOwnerMismatch)
				}
				stack = append(stack, child)
				active[ref.RunID] = struct{}{}
				continue
			}
			delta, err := DecodeRunLogDelta(record.Messages)
			if err != nil {
				return nil, invalidHistoryError(err)
			}
			delta, err = transformPrefixMessages(delta, frame.ref)
			if err != nil {
				return nil, err
			}
			messages = append(messages, delta...)
			continue
		}
		record, err := frame.nextRunRecord(ctx, store)
		if err != nil {
			return nil, err
		}
		if record == nil {
			delete(active, frame.ref.RunID)
			stack = stack[:len(stack)-1]
			continue
		}
		delta, err := decodeTranscriptMessagesDelta(record)
		if err != nil {
			return nil, invalidHistoryError(err)
		}
		delta, err = transformPrefixMessages(delta, frame.ref)
		if err != nil {
			return nil, err
		}
		messages = append(messages, delta...)
	}
	// Every reference shares this owner. Do not return readable history if a
	// concurrent purge removed it while earlier pages were being expanded.
	if _, err := store.LoadRun(ctx, runID); err != nil {
		return nil, err
	}
	return messages, nil
}

// loadPrefixFrame validates the exact committed end before reading any seed.
// An empty SeedEndID denotes an ordinary literal run history, not a request to
// infer or repair an absent publication.
func loadPrefixFrame(ctx context.Context, store transcriptPrefixStore, ref storage.HistoryPrefix) (*prefixFrame, error) {
	meta, err := store.LoadRun(ctx, ref.RunID)
	if err != nil {
		return nil, err
	}
	if meta.RunID != ref.RunID || ref.EndID == "" {
		return nil, prefixContractError("invalid history owner or position")
	}
	page, err := store.ListRunTranscriptRecords(ctx, ref.RunID, ref.EndID, "", runlogReplayPageSize)
	if err != nil {
		// No cursor is supplied here: this rejection establishes that the
		// selected saved end is invalid. Later cursor failures remain provider
		// failures because the reader obtained that cursor from the store.
		if errors.Is(err, storage.ErrInvalidTranscriptPosition) {
			return nil, invalidHistoryError(err)
		}
		return nil, fmt.Errorf("transcript: read run %q through %q: %w", ref.RunID, ref.EndID, err)
	}
	frame := &prefixFrame{
		ref: ref, meta: meta, seedDone: meta.SeedEndID == "",
		seedAfter: storage.EmptySeedEndID,
		logPage:   page, logSeen: make(map[string]struct{}), seedSeen: make(map[string]struct{}),
	}
	if err := frame.validateRunPage(""); err != nil {
		return nil, err
	}
	if !frame.seedDone {
		seed, err := store.LoadRunSeed(ctx, ref.RunID, meta.SeedEndID)
		if err != nil {
			return nil, err
		}
		if seed.EndID != meta.SeedEndID || seed.Declaration.RunID != ref.RunID ||
			seed.Declaration.AgentID != meta.AgentID || seed.Declaration.SessionID != meta.SessionID {
			return nil, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
		}
		frame.seed = seed
	}
	return frame, nil
}

func (f *prefixFrame) nextSeedRecord(ctx context.Context, store transcriptPrefixStore) (*storage.SeedRecord, error) {
	if f.seedIndex == len(f.seedPage.Records) {
		if f.seedLoaded && f.seedPage.NextCursor == "" {
			if f.seedAfter != f.seed.EndID || (f.seed.Source != nil && !f.sourceSeen) {
				return nil, prefixContractError("published seed is incomplete")
			}
			f.seedDone = true
			return nil, nil
		}
		cursor := f.seedPage.NextCursor
		page, err := store.ListRunSeedRecords(ctx, f.ref.RunID, f.seed.EndID, cursor, runlogReplayPageSize)
		if err != nil {
			return nil, err
		}
		if page.NextCursor != "" &&
			(len(page.Records) == 0 || page.NextCursor == cursor || page.NextCursor != page.Records[len(page.Records)-1].ID) {
			return nil, prefixContractError("seed page made no forward progress")
		}
		f.seedPage, f.seedIndex, f.seedLoaded = page, 0, true
		if len(page.Records) == 0 {
			if f.seedAfter != f.seed.EndID || (f.seed.Source != nil && !f.sourceSeen) {
				return nil, prefixContractError("published seed ended before its final position")
			}
			f.seedDone = true
			return nil, nil
		}
	}
	record := f.seedPage.Records[f.seedIndex]
	f.seedIndex++
	if record.ID == "" || record.PreviousID != f.seedAfter ||
		(len(record.Messages) > 0) == (record.Prefix != nil) {
		return nil, prefixContractError("invalid seed record or ordering")
	}
	if _, duplicate := f.seedSeen[record.ID]; duplicate {
		return nil, prefixContractError("seed repeated record %q", record.ID)
	}
	f.seedSeen[record.ID] = struct{}{}
	f.seedAfter = record.ID
	return &record, nil
}

func (f *prefixFrame) nextRunRecord(ctx context.Context, store transcriptPrefixStore) (*runlog.Event, error) {
	if f.logIndex == len(f.logPage.Events) {
		cursor := f.logPage.NextCursor
		if cursor == "" {
			return nil, nil
		}
		page, err := store.ListRunTranscriptRecords(ctx, f.ref.RunID, f.ref.EndID, cursor, runlogReplayPageSize)
		if err != nil {
			return nil, err
		}
		f.logPage, f.logIndex = page, 0
		if err := f.validateRunPage(cursor); err != nil {
			return nil, err
		}
		if len(page.Events) == 0 {
			return nil, nil
		}
	}
	record := f.logPage.Events[f.logIndex]
	f.logIndex++
	if record == nil || record.RunID != f.ref.RunID || record.ID == "" ||
		string(record.AgentID) != f.meta.AgentID || record.SessionID != f.meta.SessionID ||
		!isTranscriptRunLogType(record.Type) {
		return nil, prefixContractError("invalid transcript record for run %q", f.ref.RunID)
	}
	if _, duplicate := f.logSeen[record.ID]; duplicate {
		return nil, prefixContractError("store repeated record %q", record.ID)
	}
	f.logSeen[record.ID] = struct{}{}
	return record, nil
}

func (f *prefixFrame) validateRunPage(cursor string) error {
	page := f.logPage
	if page.NextCursor != "" {
		if len(page.Events) == 0 || page.NextCursor == cursor {
			return prefixContractError("run page made no forward progress")
		}
		last := page.Events[len(page.Events)-1]
		if last == nil || page.NextCursor != last.ID {
			return prefixContractError("run cursor does not identify the last returned record")
		}
	}
	return nil
}

// transformPrefixMessages applies composed exclusions to newly decoded
// literals. No already-expanded ancestor transcript is copied.
func transformPrefixMessages(messages []*model.Message, ref storage.HistoryPrefix) ([]*model.Message, error) {
	out := messages[:0]
	for _, message := range messages {
		if message == nil {
			return nil, invalidHistoryError(errors.New("nil message in stored transcript"))
		}
		if !ref.ExcludeSystem || message.Role != model.ConversationRoleSystem {
			out = append(out, message)
		}
	}
	if ref.ExcludeReasoning {
		return WithoutCompletedReasoning(out)
	}
	return out, nil
}

func prefixContractError(format string, args ...any) error {
	return storage.NewContractError(fmt.Errorf("transcript: "+format, args...))
}

// invalidHistoryError keeps the permanent storage classification and its cause
// while identifying a defect in saved history, rather than a failed store read.
func invalidHistoryError(cause error) error {
	return storage.NewContractError(fmt.Errorf("%w: %w", ErrInvalidHistory, cause))
}
