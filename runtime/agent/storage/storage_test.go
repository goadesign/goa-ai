// These tests define the record fields every durable Store implementation
// must reject before assigning an identifier or writing state.
package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestValidateRunRecord(t *testing.T) {
	t.Parallel()

	valid := func() *runlog.Event {
		return &runlog.Event{
			EventKey: "event",
			RunID:    "run",
			AgentID:  agent.Ident("service.agent"),
			Type:     runlog.Type("event"),
			Payload:  []byte(`{"value":1}`),
			Timestamp: time.Date(
				2026, time.August, 29, 12, 0, 0, 0, time.UTC,
			),
		}
	}
	tests := []struct {
		name   string
		mutate func(*runlog.Event) *runlog.Event
		want   string
	}{
		{
			name: "nil",
			mutate: func(*runlog.Event) *runlog.Event {
				return nil
			},
			want: "run record is required",
		},
		{
			name: "run id",
			mutate: func(record *runlog.Event) *runlog.Event {
				record.RunID = ""
				return record
			},
			want: "run id is required",
		},
		{
			name: "agent id",
			mutate: func(record *runlog.Event) *runlog.Event {
				record.AgentID = ""
				return record
			},
			want: "agent id is required",
		},
		{
			name: "event key",
			mutate: func(record *runlog.Event) *runlog.Event {
				record.EventKey = ""
				return record
			},
			want: "event key is required",
		},
		{
			name: "type",
			mutate: func(record *runlog.Event) *runlog.Event {
				record.Type = ""
				return record
			},
			want: "record type is required",
		},
		{
			name: "payload",
			mutate: func(record *runlog.Event) *runlog.Event {
				record.Payload = nil
				return record
			},
			want: "record payload is required",
		},
		{
			name: "timestamp",
			mutate: func(record *runlog.Event) *runlog.Event {
				record.Timestamp = time.Time{}
				return record
			},
			want: "record timestamp is required",
		},
		{
			name: "timestamp precision",
			mutate: func(record *runlog.Event) *runlog.Event {
				record.Timestamp = record.Timestamp.Add(time.Nanosecond)
				return record
			},
			want: "record timestamp must use millisecond precision",
		},
	}

	require.NoError(t, ValidateRunRecord(valid()))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.EqualError(t, ValidateRunRecord(test.mutate(valid())), test.want)
		})
	}
}
