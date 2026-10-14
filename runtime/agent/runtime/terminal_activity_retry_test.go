// These tests keep cancellation provenance reads and terminal writes inside the
// same retrying activity, including a lost reply after the store commits.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/engine/contract"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/content"
)

type terminalActivityFaultStore struct {
	storage.Store
	failLoad        bool
	loseWriteReply  bool
	loadCalls       int
	terminalRecords []*runlog.Event
	terminalResults []*genrunoutput.OperationResult
}

func (s *terminalActivityFaultStore) LoadRun(ctx context.Context, id string) (session.RunMeta, error) {
	s.loadCalls++
	if s.failLoad {
		s.failLoad = false
		return session.RunMeta{}, errors.New("temporary cancellation read failure")
	}
	return s.Store.LoadRun(ctx, id)
}

func (s *terminalActivityFaultStore) RecordRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.AppendResult, error) {
	frozen := *command.Record
	frozen.Payload = append([]byte(nil), command.Record.Payload...)
	s.terminalRecords = append(s.terminalRecords, &frozen)
	s.terminalResults = append(s.terminalResults, command.OperationResult)
	result, err := s.Store.RecordRunTerminal(ctx, command)
	if err == nil && s.loseWriteReply {
		s.loseWriteReply = false
		return storage.AppendResult{}, errors.New("terminal write reply lost")
	}
	return result, err
}

func TestTerminalActivityRetriesCancellationReadAndLostWriteReply(t *testing.T) {
	base := newTestStore()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("test.agent"))}
	admitRunForTest(t, base, session.RunMeta{Subject: subject, RunID: "terminal-run", Status: session.RunStatusRunning})
	store := &terminalActivityFaultStore{Store: base}
	adapter := engineinmem.New()
	rt := New(store, WithEngine(adapter))
	cancellation, err := buildCancellationRecord(&RecordActivityInput{RunID: "terminal-run", Subject: subject, TimestampMS: time.Now().UnixMilli()}, "custom-owner-reason")
	require.NoError(t, err)
	_, err = rt.cancelRun(t.Context(), cancellation)
	require.NoError(t, err)
	store.failLoad, store.loseWriteReply = true, true
	decidedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	terminal, err := buildRunTerminalCommand(&RunInput{RunID: "terminal-run", Subject: subject}, "", runStatusCanceled, context.Canceled, nil, recordDispatchMetadata{TimestampMS: decidedAt.UnixMilli()})
	require.NoError(t, err)
	require.NoError(t, adapter.RegisterStorageActivity(t.Context(), storageActivityName, engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}}, rt.executeStorageCommand))
	require.NoError(t, adapter.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{Name: "terminal.retry", Handler: func(wf engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
		_, err := rt.executeStorageWithRetry(wf.Detached().Context(), &api.StorageActivityCommand{Terminal: terminal})
		return nil, err
	}}))
	handle, err := adapter.StartWorkflow(t.Context(), engine.WorkflowStartRequest{ID: "terminal-run", Workflow: "terminal.retry", TaskQueue: "test", Input: &api.RunInput{Subject: subject, RunID: "terminal-run"}})
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 3, store.loadCalls)
	require.Len(t, store.terminalRecords, 2)
	assert.Equal(t, store.terminalRecords[0], store.terminalRecords[1])
	assert.Equal(t, decidedAt, store.terminalRecords[0].Timestamp)
	decoded, err := hooks.DecodeRunlogEvent(store.terminalRecords[0])
	require.NoError(t, err)
	assert.Equal(t, "custom-owner-reason", decoded.(*hooks.RunCompletedEvent).Cancellation.Reason)
	meta, err := base.LoadRun(t.Context(), "terminal-run")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, meta.Status)
	page, err := base.ListRunRecords(t.Context(), "terminal-run", "", 10)
	require.NoError(t, err)
	assert.Len(t, page.Events, 3)
}

func TestTerminalOperationFailureSurvivesLostWriteAndCompetingRepair(t *testing.T) {
	for _, status := range []string{runStatusSuccess, runStatusFailed} {
		t.Run(status, func(t *testing.T) {
			base := newTestStore()
			subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("test.agent"))}
			admitRunForTest(t, base, session.RunMeta{Subject: subject, RunID: "result-run", Status: session.RunStatusRunning})
			store := &terminalActivityFaultStore{Store: base, loseWriteReply: true}
			rt := New(store)
			output := &api.RunOutput{FinalToolResult: &api.ToolEvent{Failure: &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError("Selected operation unavailable"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}, Blocks: content.Blocks{&content.TextContent{Text: "Retained partial evidence"}}}}
			var workflowErr error
			if status == runStatusFailed {
				workflowErr = errors.New("workflow returned selected failure")
			}
			selectedResult, resultErr := operationResultFromRunOutput(output)
			require.NoError(t, resultErr)
			terminal, err := buildRunTerminalCommand(&RunInput{RunID: "result-run", Subject: subject}, "", status, workflowErr, selectedResult, recordDispatchMetadata{TimestampMS: time.Now().UnixMilli()})
			require.NoError(t, err)
			_, err = rt.storageCommandUntilApplied(t.Context(), &api.StorageActivityCommand{Terminal: terminal})
			require.NoError(t, err)
			require.Len(t, store.terminalRecords, 2)
			assert.Equal(t, store.terminalRecords[0], store.terminalRecords[1])
			require.Len(t, store.terminalResults, 2)
			assert.Equal(t, store.terminalResults[0], store.terminalResults[1])
			failure, ok := store.terminalResults[0].Outcome.AsFailure()
			require.True(t, ok)
			require.Len(t, failure.Blocks, 1)
			block, ok := failure.Blocks[0].Content.AsText()
			require.True(t, ok)
			assert.Equal(t, "Retained partial evidence", block.Text)
			competing := hooks.NewRunCompletedEvent("result-run", subject, "", runStatusFailed, run.PhaseFailed, nil, errors.New("later failure"), nil)
			competingRecord, err := prepareHookRecordInputWithMetadata(competing, "", recordDispatchMetadata{TimestampMS: time.Now().Add(time.Second).UnixMilli()})
			require.NoError(t, err)
			meta, err := base.LoadRun(t.Context(), "result-run")
			require.NoError(t, err)
			command, _, err := runTerminalStorageCommand(&api.RunTerminalCommand{Record: competingRecord}, meta)
			require.NoError(t, err)
			result, err := base.RepairRunTerminal(t.Context(), command)
			require.NoError(t, err)
			assert.Equal(t, storage.RunRepairDifferentTerminal, result.Outcome)
			page, err := base.ListRunRecords(t.Context(), "result-run", "", 10)
			require.NoError(t, err)
			require.Len(t, page.Events, 2)
			assert.Equal(t, store.terminalRecords[0].Payload, page.Events[1].Payload)
		})
	}
}

// A result that crosses the existing engine boundary must remain representable
// when the same selected operation is saved in its terminal event.
func TestTerminalOperationResultPreservesAcceptedOutputBudget(t *testing.T) {
	for _, labelBytes := range []int{0, 300_000} {
		t.Run(fmt.Sprintf("labels=%d", labelBytes), func(t *testing.T) {
			subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
			labels := map[string]string{"context": strings.Repeat("x", labelBytes)}
			output := &api.RunOutput{Subject: subject, RunID: "large-result", FinalToolResult: &api.ToolEvent{Name: "records.read", ToolCallID: "selected-call", Result: rawjson.Message(`"` + strings.Repeat("x", 800_000) + `"`)}}
			require.NoError(t, validateToolActivityOutputBudget(&api.ToolOutput{Payload: output.FinalToolResult.Result}))
			accepted, err := contract.CopyRunOutput(output)
			require.NoError(t, err)
			metadata := recordDispatchMetadata{EventKey: "size-proof", TimestampMS: time.Now().UnixMilli()}
			started, err := prepareHookRecordInputWithMetadata(hooks.NewRunStartedEvent(output.RunID, subject, "", "", "", labels), "", metadata)
			require.NoError(t, err)
			startCommand, err := runStartStorageCommand(&api.RunInput{Subject: subject, RunID: output.RunID, SeedEndID: storage.EmptySeedEndID}, [32]byte{1}, []*RecordActivityInput{started})
			require.NoError(t, err)
			converter := workflowcodec.NewDataConverter()
			_, err = converter.ToPayload(startCommand)
			require.NoError(t, err)
			toolEvent := hooks.NewToolResultReceivedEvent(output.RunID, subject, "", output.RunID, "records.read", "selected-call", "", output.FinalToolResult.Result, nil, nil, "", nil, 0, nil, nil)
			toolRecord, err := prepareHookRecordInputWithMetadata(toolEvent, "", metadata)
			require.NoError(t, err)
			_, err = converter.ToPayload(appendStorageCommand(toolRecord))
			require.NoError(t, err)
			previousTerminal, err := prepareHookRecordInputWithMetadata(hooks.NewRunCompletedEvent(output.RunID, subject, "", runStatusSuccess, run.PhaseCompleted, labels, nil, nil), "", metadata)
			require.NoError(t, err)
			_, err = converter.ToPayload(&api.StorageActivityCommand{Terminal: &api.RunTerminalCommand{Record: previousTerminal}})
			require.NoError(t, err)
			t.Logf("accepted start payload=%d, tool-result payload=%d, prior terminal payload=%d, raw selected result=%d", len(started.Payload), len(toolRecord.Payload), len(previousTerminal.Payload), len(output.FinalToolResult.Result))
			selectedResult, resultErr := operationResultFromRunOutput(accepted)
			require.NoError(t, resultErr)
			terminal, err := buildRunTerminalCommand(&RunInput{RunID: output.RunID, Subject: subject}, "", runStatusSuccess, nil, selectedResult, metadata)
			require.NoError(t, err)
			_, err = converter.ToPayload(&api.StorageActivityCommand{Terminal: terminal})
			require.NoError(t, err)
			decoded, err := genrunoutput.DecodeOperationResult(terminal.OperationResult)
			require.NoError(t, err)
			actual, ok := decoded.Outcome.AsSuccess()
			require.True(t, ok)
			value, ok := actual.Value.AsJSON()
			require.True(t, ok)
			assert.Equal(t, []byte(output.FinalToolResult.Result), []byte(value))
		})
	}
}
