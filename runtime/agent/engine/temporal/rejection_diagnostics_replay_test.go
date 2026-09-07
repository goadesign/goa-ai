package temporal

// These synthetic histories run through the production workflow replayer.
// Legacy rejection records remain hash-only; new records add exact reason text
// without changing recovery, activity ordering, or attributed token usage.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestProductionWorkflowReplaysRejectionDiagnosticVersions(t *testing.T) {
	for _, format := range []struct{ version, omitted string }{
		{},
		{version: errorevidence.LegacyReasonVersion},
		{version: errorevidence.LegacyReasonVersion, omitted: "size_limit"},
		{version: errorevidence.LegacyReasonVersion, omitted: "invalid_utf8"},
		{version: errorevidence.ReasonVersion},
	} {
		version := format.version
		for _, recovery := range []bool{false, true} {
			name := "legacy"
			if version != "" {
				name = version + "/" + format.omitted
			}
			if recovery {
				name += "/recovery"
			} else {
				name += "/terminal"
			}
			t.Run(name, func(t *testing.T) {
				plannerStub, handler := productionReplayWorkflow(t)
				reason := "field[7]: invalid value"
				if version == errorevidence.ReasonVersion {
					reason = strings.Repeat(reason+"\n", 400)
				}
				switch format.omitted {
				case "size_limit":
					reason = strings.Repeat("x", 3073)
				case "invalid_utf8":
					reason = string([]byte{0xff})
				}
				digest, size := errorevidence.FingerprintText(reason)
				failure := &api.OutputContractFailure{Origin: planner.OutputContractOriginPlanner, ReasonSHA256: digest, ReasonSize: int64(size)}
				if version != "" {
					failure.ReasonVersion, failure.Reason = version, reason
				}
				if format.omitted != "" {
					failure.Reason, failure.ReasonOmitted = "", format.omitted
				}
				if recovery {
					failure.Origin = planner.OutputContractOriginModel
					failure.ModelResponsePresent = true
					failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV2
					failure.ModelResponseSHA256, size = errorevidence.FingerprintText("rejected response")
					failure.ModelResponseSize = int64(size)
					failure.ModelOutputRecovery = &api.ModelOutputRecovery{Kind: planner.ModelOutputRecoveryAnswer, Correction: "Correct field[7]."}
				}
				usage := model.TokenUsage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7}
				usageEvent := hooks.NewUsageEvent(productionReplayRunID, productionReplayAgentID, productionReplaySessionID, usage)
				usagePayload, err := hooks.EncodeRecordPayload(usageEvent)
				require.NoError(t, err)
				first := &api.PlanActivityOutput{
					PublicationBatchID:    "00000000-0000-4000-8000-000000000001",
					OutputContractFailure: failure,
					Usage:                 usage,
					PlannerEvents:         []*api.PlannerEventRecord{{Type: hooks.Usage, Payload: usagePayload}},
				}
				var history *historypb.History
				if recovery {
					history = syntheticProductionReplayHistory(t, first, true)
				} else {
					history = terminalRejectionReplayHistory(t, first)
					if version != errorevidence.ReasonVersion {
						// The SDK matches a failed-workflow event by kind, not by
						// its saved failure bytes. This proves replay compatibility;
						// temporalerrors separately pins stored failure bytes.
						completedID := int64(len(history.Events))
						history.Events = append(history.Events, &historypb.HistoryEvent{
							EventId:   completedID + 1,
							EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED,
							Attributes: &historypb.HistoryEvent_WorkflowExecutionFailedEventAttributes{
								WorkflowExecutionFailedEventAttributes: &historypb.WorkflowExecutionFailedEventAttributes{
									WorkflowTaskCompletedEventId: completedID,
									Failure: &failurepb.Failure{
										Message: "historical terminal diagnostic",
										FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
											Type: "goa_ai.output_contract_error", NonRetryable: true,
											Details: &commonpb.Payloads{Payloads: []*commonpb.Payload{{
												Metadata: map[string][]byte{"encoding": []byte("json/plain")},
												Data:     []byte(`{"Origin":"planner"}`),
											}}},
										}},
									},
								},
							},
						})
					}
				}
				history = deserializeReplayHistory(t, history)
				output := replayProductionWorkflow(t, handler, history)
				assert.Zero(t, plannerStub.calls.Load())
				assert.NotContains(t, scheduledActivityNames(history), productionReplayExecute)
				assert.Equal(t, []model.TokenUsage{usage}, recordedUsageEvents(t, history))
				if recovery {
					require.NotNil(t, output)
					require.NotNil(t, output.Usage)
					assert.Equal(t, usage, *output.Usage)
				}
			})
		}
	}
}

// rejectionReplayPublication prepares fixture payloads independently from the
// production publication builder. Existing non-rejection fixtures stay intact.
func rejectionReplayPublication(t *testing.T, first *api.PlanActivityOutput) []*api.PlannerEventRecord {
	t.Helper()
	if first.OutputContractFailure == nil {
		return first.PlannerEvents
	}
	failure := first.OutputContractFailure
	var event hooks.Event
	if failure.Origin == planner.OutputContractOriginModel {
		rejected, err := hooks.NewModelOutputRejectedEvent(productionReplayRunID, productionReplayAgentID, productionReplaySessionID,
			failure.ReasonSHA256, failure.ReasonSize, failure.ModelOutputValidationKind, failure.ModelResponsePresent,
			failure.ModelResponseFingerprintVersion, failure.ModelResponseSHA256, failure.ModelResponseSize)
		require.NoError(t, err)
		rejected.ReasonVersion, rejected.Reason, rejected.ReasonOmitted = failure.ReasonVersion, failure.Reason, failure.ReasonOmitted
		event = rejected
	} else {
		rejected, err := hooks.NewPlannerOutputRejectedEvent(productionReplayRunID, productionReplayAgentID, productionReplaySessionID,
			failure.ReasonSHA256, failure.ReasonSize)
		require.NoError(t, err)
		rejected.ReasonVersion, rejected.Reason, rejected.ReasonOmitted = failure.ReasonVersion, failure.Reason, failure.ReasonOmitted
		event = rejected
	}
	payload, err := hooks.EncodeRecordPayload(event)
	require.NoError(t, err)
	events := append([]*api.PlannerEventRecord(nil), first.PlannerEvents...)
	return append(events, &api.PlannerEventRecord{Type: event.Type(), Payload: payload})
}

// terminalRejectionReplayHistory ends after the terminal write's workflow task.
// The replayer must produce the failure command without running an activity.
func terminalRejectionReplayHistory(t *testing.T, first *api.PlanActivityOutput) *historypb.History {
	t.Helper()
	history := syntheticProductionReplayHistory(t, first, false)
	history.Events = history.Events[:28]
	appendResult, err := NewAgentDataConverter().ToPayloads(&api.StorageActivityResult{Append: &api.AppendRecordsResult{}})
	require.NoError(t, err)
	terminalResult, err := NewAgentDataConverter().ToPayloads(&api.StorageActivityResult{Terminal: &api.RecordWriteResult{}})
	require.NoError(t, err)
	publication := plannerPublicationInput(t, rejectionReplayPublication(t, first))
	for index, result := range []*commonpb.Payloads{appendResult, terminalResult} {
		id := int64(len(history.Events) + 1)
		var input *commonpb.Payloads
		if index == 0 {
			input = publication
		}
		history.Events = append(history.Events,
			activityTaskScheduledEvent(id, productionReplayRecord, input),
			activityTaskStartedEvent(id+1, id),
			activityTaskCompletedEvent(id+2, id, id+1, result),
			workflowTaskScheduledEvent(id+3),
			workflowTaskStartedEvent(id+4),
			workflowTaskCompletedEvent(id+5, id+3, id+4),
		)
	}
	return history
}
