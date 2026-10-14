// Terminal sender tests compare the actual planner and tool-result messages
// with the completion request that must preserve their selected result.
package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/engine/contract"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/toolerrors"
)

func TestTerminalSenderPreservesNativePlannerBoundary(t *testing.T) {
	converter := workflowcodec.NewDataConverter()
	makePlan := func(size int) *api.PlanActivityOutput {
		return &api.PlanActivityOutput{PublicationBatchID: "publication-1", Result: &api.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`"` + strings.Repeat("x", size) + `"`)}}}
	}
	accepted := func(size int) bool {
		plan := makePlan(size)
		if checkPlanActivityOutputBudget(plan) != nil {
			return false
		}
		_, err := converter.ToPayload(plan)
		return err == nil
	}
	low, high := 0, engine.MaxPayloadBytes
	for low < high {
		middle := low + (high-low+1)/2
		if accepted(middle) {
			low = middle
		} else {
			high = middle - 1
		}
	}
	require.True(t, accepted(low))
	require.False(t, accepted(low+1))
	plan := makePlan(low)
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
	output, err := contract.CopyRunOutput(&api.RunOutput{Subject: subject, RunID: "selected-run", FinalToolResult: finalToolResultEvent("records.read", plan.Result.FinalToolResult)})
	require.NoError(t, err)
	planPayload, err := converter.ToPayload(plan)
	require.NoError(t, err)
	outputPayload, err := converter.ToPayload(output)
	require.NoError(t, err)
	selectedResult, resultErr := operationResultFromRunOutput(output)
	require.NoError(t, resultErr)
	command, err := buildRunTerminalCommand(&RunInput{RunID: output.RunID, Subject: subject}, "", runStatusSuccess, nil, selectedResult, recordDispatchMetadata{EventKey: "completion-1", TimestampMS: 1_700_000_000_000})
	require.NoError(t, err)
	t.Logf("largest accepted planner result=%d; planner data=%d; output data=%d; terminal result=%d; terminal metadata=%d", low+2, len(planPayload.Data), len(outputPayload.Data), len(command.OperationResult), len(command.Record.Payload))
	_, err = converter.ToPayload(&api.StorageActivityCommand{Terminal: command})
	require.NoError(t, err)
}

func TestTerminalSenderPreservesAcceptedServiceFailure(t *testing.T) {
	converter := workflowcodec.NewDataConverter()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "registry", Tool: "records.read"})}
	metadata := recordDispatchMetadata{EventKey: "completion-1", TimestampMS: 1_700_000_000_000}
	failure := &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: toolerrors.New("x"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
	initial := hooks.NewToolResultReceivedEvent("selected-run", subject, "", "selected-run", "records.read", "selected-call", "", nil, nil, nil, "", nil, 0, nil, failure)
	baseline, err := prepareHookRecordInputWithMetadata(initial, "", metadata)
	require.NoError(t, err)
	largestMessage := maxHookPayloadBytes - len(baseline.Payload) + 1
	for _, size := range []int{600_000, largestMessage} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			failure := &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: toolerrors.New(strings.Repeat("x", size)), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
			toolOutput := &api.ToolOutput{Failure: failure}
			require.NoError(t, validateToolActivityOutputBudget(toolOutput))
			_, err := converter.ToPayload(toolOutput)
			require.NoError(t, err)
			toolEvent := hooks.NewToolResultReceivedEvent("selected-run", subject, "", "selected-run", "records.read", "selected-call", "", nil, nil, nil, "", nil, 0, nil, failure)
			record, err := prepareHookRecordInputWithMetadata(toolEvent, "", metadata)
			require.NoError(t, err)
			if size == largestMessage {
				assert.Len(t, record.Payload, maxHookPayloadBytes)
				failure.Error.Message += "x"
				oversized := hooks.NewToolResultReceivedEvent("selected-run", subject, "", "selected-run", "records.read", "selected-call", "", nil, nil, nil, "", nil, 0, nil, failure)
				_, err = prepareHookRecordInputWithMetadata(oversized, "", metadata)
				require.Error(t, err)
				failure.Error.Message = failure.Error.Message[:size]
			}
			_, err = converter.ToPayload(appendStorageCommand(record))
			require.NoError(t, err)
			output, err := contract.CopyRunOutput(&api.RunOutput{Subject: subject, RunID: "selected-run", FinalToolResult: &api.ToolEvent{Name: "records.read", ToolCallID: "selected-call", Failure: failure}, ToolCount: 1})
			require.NoError(t, err)
			selectedResult, resultErr := operationResultFromRunOutput(output)
			require.NoError(t, resultErr)
			command, err := buildRunTerminalCommand(&RunInput{RunID: output.RunID, Subject: subject}, "", runStatusFailed, &selectedOperationError{cause: failure.Error}, selectedResult, metadata)
			require.NoError(t, err)
			t.Logf("accepted service result event=%d; terminal result=%d; terminal metadata=%d", len(record.Payload), len(command.OperationResult), len(command.Record.Payload))
			_, err = converter.ToPayload(&api.StorageActivityCommand{Terminal: command})
			require.NoError(t, err)
			labels := map[string]string{"saved-context": strings.Repeat("x", 300_000)}
			start, err := prepareHookRecordInputWithMetadata(hooks.NewRunStartedEvent(output.RunID, subject, "", "", "", labels), "", metadata)
			require.NoError(t, err)
			startCommand, err := runStartStorageCommand(&api.RunInput{Subject: subject, RunID: output.RunID, SeedEndID: "published"}, [32]byte{1}, []*RecordActivityInput{start})
			require.NoError(t, err)
			_, err = converter.ToPayload(startCommand)
			require.NoError(t, err)
			stored, _, err := runTerminalStorageCommand(command, session.RunMeta{Labels: labels})
			require.NoError(t, err)
			expected, err := hooks.EncodeToRecordInput(hooks.NewRunCompletedEvent(output.RunID, subject, "", runStatusFailed, run.PhaseFailed, labels, fmt.Errorf("registered tool failed: %w", failure.Error), nil), hooks.EncodeOptions{EventKey: metadata.EventKey, TimestampMS: metadata.TimestampMS})
			require.NoError(t, err)
			assert.Equal(t, []byte(expected.Payload), []byte(stored.Record.Payload))
			t.Logf("restored event with separately accepted labels=%d", len(stored.Record.Payload))
		})
	}
}
