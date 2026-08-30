// These tests prove that ordinary history writes cannot bypass the operations
// that store lifecycle records and matching run state together.
package lifecycle

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestValidateOrdinaryRunRecordRejectsLifecycleTypes(t *testing.T) {
	t.Parallel()

	for _, recordType := range []runlog.Type{
		hooks.RunStarted,
		hooks.ChildRunLinked,
		storage.CancellationRecordType,
		hooks.RunSuspended,
		hooks.RunCompleted,
	} {
		t.Run(string(recordType), func(t *testing.T) {
			t.Parallel()
			record := validOrdinaryRecord()
			record.Type = recordType

			require.EqualError(
				t,
				ValidateOrdinaryRunRecord(record),
				`record type "`+string(recordType)+`" requires a lifecycle operation`,
			)
		})
	}

	require.NoError(t, ValidateOrdinaryRunRecord(validOrdinaryRecord()))
}

// validOrdinaryRecord returns the smallest complete non-lifecycle record.
func validOrdinaryRecord() *runlog.Event {
	return &runlog.Event{
		EventKey: "event",
		RunID:    "run",
		AgentID:  agent.Ident("service.agent"),
		Type:     runlog.Type("planner_note"),
		Payload:  []byte(`{"value":1}`),
		Timestamp: time.Date(
			2026, time.August, 29, 12, 0, 0, 0, time.UTC,
		),
	}
}
