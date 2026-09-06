package temporal

// These synthetic histories run through the production workflow replayer.
// Legacy rejection records remain hash-only; new records add exact reason text
// without changing recovery, activity ordering, or attributed token usage.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	historypb "go.temporal.io/api/history/v1"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestProductionWorkflowReplaysRejectionDiagnosticVersions(t *testing.T) {
	for _, current := range []bool{false, true} {
		for _, recovery := range []bool{false, true} {
			name := "legacy"
			if current {
				name = "current"
			}
			if recovery {
				name += "/recovery"
			} else {
				name += "/terminal"
			}
			t.Run(name, func(t *testing.T) {
				plannerStub, handler := productionReplayWorkflow(t)
				reason := "field[7]: invalid value"
				digest, size := errorevidence.FingerprintText(reason)
				failure := &api.OutputContractFailure{Origin: planner.OutputContractOriginPlanner, ReasonSHA256: digest, ReasonSize: int64(size)}
				if current {
					failure.ReasonVersion, failure.Reason = errorevidence.ReasonVersion, reason
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
