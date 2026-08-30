package runtime

import (
	"context"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
)

// ResolvePromptRefs returns prompt refs for the run and all linked child runs.
//
// Child links and prompt versions are derived from the canonical session run
// log instead of duplicated in mutable session metadata.
func (r *Runtime) ResolvePromptRefs(ctx context.Context, runID string) ([]prompt.PromptRef, error) {
	if _, err := r.Store.LoadRun(ctx, runID); err != nil {
		return nil, err
	}
	queue := []string{runID}
	seenRuns := map[string]struct{}{runID: {}}
	seenRefs := make(map[prompt.PromptRef]struct{})
	var refs []prompt.PromptRef
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		runRefs, children, err := r.listRunPromptData(ctx, current)
		if err != nil {
			return nil, err
		}
		for _, ref := range runRefs {
			if _, seen := seenRefs[ref]; seen {
				continue
			}
			seenRefs[ref] = struct{}{}
			refs = append(refs, ref)
		}
		for _, childID := range children {
			if _, seen := seenRuns[childID]; seen {
				continue
			}
			seenRuns[childID] = struct{}{}
			queue = append(queue, childID)
		}
	}
	return refs, nil
}

// listRunPromptData pages one reachable run and decodes only the prompt and
// child-link records needed by ResolvePromptRefs. Malformed records belonging
// to unrelated runs are never loaded.
func (r *Runtime) listRunPromptData(ctx context.Context, runID string) ([]prompt.PromptRef, []string, error) {
	var refs []prompt.PromptRef
	var children []string
	cursor := ""
	for {
		page, err := r.Store.ListRunRecords(ctx, runID, cursor, 500)
		if err != nil {
			return nil, nil, err
		}
		for _, record := range page.Events {
			if record.Type != hooks.PromptRendered && record.Type != hooks.ChildRunLinked {
				continue
			}
			event, err := hooks.DecodeFromRecordInput(&RecordActivityInput{
				Type: record.Type, RunID: record.RunID, AgentID: record.AgentID,
				SessionID: record.SessionID, TurnID: record.TurnID,
				TimestampMS: record.Timestamp.UnixMilli(), Payload: record.Payload,
			})
			if err != nil {
				return nil, nil, err
			}
			switch event := event.(type) {
			case *hooks.PromptRenderedEvent:
				refs = append(refs, prompt.PromptRef{ID: event.PromptID, Version: event.Version})
			case *hooks.ChildRunLinkedEvent:
				children = append(children, event.ChildRunID)
			}
		}
		if page.NextCursor == "" {
			return refs, children, nil
		}
		cursor = page.NextCursor
	}
}
