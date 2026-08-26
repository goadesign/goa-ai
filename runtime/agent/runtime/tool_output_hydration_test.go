// These tests verify that tool-output hydration rejects malformed canonical
// run-log pages before decoding planner-visible tool state.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/runlog"
)

type nilEventRunlog struct{}

// Append panics because hydration must only read the test store.
func (nilEventRunlog) Append(context.Context, *runlog.Event) (runlog.AppendResult, error) {
	panic("Append must not be called")
}

// List returns the impossible nil event that hydration must reject.
func (nilEventRunlog) List(context.Context, string, string, int) (runlog.Page, error) {
	return runlog.Page{Events: []*runlog.Event{nil}}, nil
}

func TestLoadCanonicalToolEventsRejectsNilStoredEvent(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{RunEventStore: nilEventRunlog{}}
	_, err := runtime.loadCanonicalToolEvents(
		context.Background(),
		"run-1",
		map[string]struct{}{"call-1": {}},
	)

	require.EqualError(
		t,
		err,
		`runtime: nil event from run log during tool hydration (run_id=run-1 page_cursor="" index=0)`,
	)
}
