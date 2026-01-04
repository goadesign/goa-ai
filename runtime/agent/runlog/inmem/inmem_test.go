package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestStoreAppendAndList(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		err := s.Append(ctx, &runlog.Event{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Type:      "event",
			Payload:   []byte(`{}`),
			Timestamp: time.Unix(int64(i+1), 0).UTC(),
		})
		require.NoError(t, err)
	}

	page1, err := s.List(ctx, "run-1", "", 2)
	require.NoError(t, err)
	require.Len(t, page1.Events, 2)
	require.Equal(t, "1", page1.Events[0].ID)
	require.Equal(t, "2", page1.Events[1].ID)
	require.Equal(t, "2", page1.NextCursor)

	page2, err := s.List(ctx, "run-1", page1.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, page2.Events, 1)
	require.Equal(t, "3", page2.Events[0].ID)
	require.Empty(t, page2.NextCursor)
}

func TestStoreListValidation(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()

	_, err := s.List(ctx, "", "", 10)
	require.Error(t, err)

	_, err = s.List(ctx, "run-1", "", 0)
	require.Error(t, err)

	_, err = s.List(ctx, "run-1", "not-an-int", 10)
	require.Error(t, err)
}
