// These tests keep the selected result independent of the event payload while
// requiring the event, final status, and result to share one immutable winner.
package inmem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestTerminalSelectedResultSharesFirstWinner(t *testing.T) {
	for _, repairFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "workflow_first", true: "repair_first"}[repairFirst], func(t *testing.T) {
			store, start := runningRootStore(t)
			success := &genrunoutput.OperationSuccess{Value: genrunoutput.NewOperationValueJSON(genrunoutput.OperationValueBranchJSON(`{"count":9007199254740993}`))}
			result := &genrunoutput.OperationResult{Outcome: genrunoutput.NewOperationOutcomeSuccess(success)}
			command := storage.RunTerminal{RunID: start.RunID, Status: session.RunStatusCompleted, Record: completedRecord(t, "terminal", start, "success", nil), OperationResult: result}
			encoded, err := genrunoutput.EncodeOperationResult(result)
			require.NoError(t, err)
			if repairFirst {
				saved, err := store.RepairRunTerminal(t.Context(), command)
				require.NoError(t, err)
				assert.Equal(t, storage.RunRepairStored, saved.Outcome)
			} else {
				saved, err := store.RecordRunTerminal(t.Context(), command)
				require.NoError(t, err)
				assert.True(t, saved.Inserted)
			}
			saved, err := store.RecordRunTerminal(t.Context(), command)
			require.NoError(t, err)
			assert.False(t, saved.Inserted)
			assert.Equal(t, encoded, store.terminalResults[start.RunID])
			success.Value = genrunoutput.NewOperationValueEmpty(&genrunoutput.EmptyOperationResult{})
			assert.Equal(t, encoded, store.terminalResults[start.RunID], "caller mutation cannot change saved result")
			_, err = store.RecordRunTerminal(t.Context(), command)
			assert.ErrorIs(t, err, session.ErrRunTerminalConflict) //nolint:testifylint // Repair independently checks the competing outcome against the retained terminal result.
			competing, err := store.RepairRunTerminal(t.Context(), command)
			require.NoError(t, err)
			assert.Equal(t, storage.RunRepairDifferentTerminal, competing.Outcome)
			command.OperationResult = nil
			_, err = store.RecordRunTerminal(t.Context(), command)
			assert.ErrorIs(t, err, session.ErrRunTerminalConflict, "absent and explicit empty are different outcomes") //nolint:testifylint // The retained result bytes must still be checked after the conflicting write.
			assert.Equal(t, encoded, store.terminalResults[start.RunID])
		})
	}
}

func TestTerminalSelectedResultRejectsMalformedBeforeClosure(t *testing.T) {
	store, start := runningRootStore(t)
	command := storage.RunTerminal{RunID: start.RunID, Status: session.RunStatusCompleted, Record: completedRecord(t, "terminal", start, "success", nil), OperationResult: &genrunoutput.OperationResult{}}
	_, err := store.RecordRunTerminal(t.Context(), command)
	require.Error(t, err)
	meta, err := store.LoadRun(t.Context(), start.RunID)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusRunning, meta.Status)
	assert.Empty(t, store.terminalResults)
}
