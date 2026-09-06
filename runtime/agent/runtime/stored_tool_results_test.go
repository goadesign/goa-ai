package runtime

// These tests read completed tool results from the runtime's existing Store,
// where full results and failures remain available after workflow completion.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/hooks"
)

// storedToolResults reads every page for one exact run and preserves recorded
// result order. The two-record page size exercises pagination in small fixtures;
// it batches reads without limiting the number of results the test can inspect.
func storedToolResults(t *testing.T, rt *Runtime, runID string) []*hooks.ToolResultReceivedEvent {
	t.Helper()
	var results []*hooks.ToolResultReceivedEvent
	cursor := ""
	for {
		page, err := rt.ListRunEvents(t.Context(), runID, cursor, 2)
		require.NoError(t, err)
		for _, record := range page.Events {
			if record.Type != hooks.ToolResultReceived {
				continue
			}
			event, err := hooks.DecodeRunlogEvent(record)
			require.NoError(t, err)
			result, ok := event.(*hooks.ToolResultReceivedEvent)
			require.True(t, ok)
			results = append(results, result)
		}
		if page.NextCursor == "" {
			return results
		}
		cursor = page.NextCursor
	}
}
