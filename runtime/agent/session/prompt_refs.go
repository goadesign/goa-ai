package session

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/prompt"
)

type promptRefKey struct {
	id      prompt.Ident
	version string
}

// ResolvePromptRefs returns the unique prompt refs used by a run and all of its
// descendant child runs.
//
// Contract:
//   - runID must be non-empty.
//   - ErrRunNotFound is returned when the root run does not exist.
//   - Prompt refs are deduplicated by (prompt_id, version) with first-seen order.
func ResolvePromptRefs(ctx context.Context, store Store, runID string) ([]prompt.PromptRef, error) {
	if store == nil {
		return nil, errors.New("session store is required")
	}
	if runID == "" {
		return nil, errors.New("run id is required")
	}

	queue := []string{runID}
	seenRuns := map[string]struct{}{
		runID: {},
	}
	seenRefs := make(map[promptRefKey]struct{}, 16)
	refs := make([]prompt.PromptRef, 0, 16)

	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]

		run, err := store.LoadRun(ctx, current)
		if err != nil {
			if errors.Is(err, ErrRunNotFound) && current == runID {
				return nil, ErrRunNotFound
			}
			return nil, fmt.Errorf("load run %q: %w", current, err)
		}
		for _, ref := range run.PromptRefs {
			key := promptRefKey{id: ref.ID, version: ref.Version}
			if _, ok := seenRefs[key]; ok {
				continue
			}
			seenRefs[key] = struct{}{}
			refs = append(refs, ref)
		}
		for _, childRunID := range run.ChildRunIDs {
			if childRunID == "" {
				return nil, fmt.Errorf("run %q has empty child run id", current)
			}
			if _, ok := seenRuns[childRunID]; ok {
				continue
			}
			seenRuns[childRunID] = struct{}{}
			queue = append(queue, childRunID)
		}
	}

	return refs, nil
}
