package runtime

// Saved checkpoints must retain the Agent that issued every call and result.
// These tests exercise storage decoding before restored call metadata changes.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCheckpointSubjectsRejectForeignSavedOwners(t *testing.T) {
	locations := []struct {
		name string
		set  func(*workflowCheckpoint, genrunidentity.RunSubject)
	}{
		{"planner call", func(c *workflowCheckpoint, subject genrunidentity.RunSubject) {
			c.Batch.Result.ToolCalls[0].Subject = subject
		}},
		{"batch call", func(c *workflowCheckpoint, subject genrunidentity.RunSubject) {
			c.Batch.Calls[0].Subject = subject
		}},
		{"recorded call", func(c *workflowCheckpoint, subject genrunidentity.RunSubject) {
			c.Batch.Records[0].Call.Subject = subject
		}},
		{"result record", func(c *workflowCheckpoint, subject genrunidentity.RunSubject) {
			c.Batch.Records[0].ResultRecord = &RecordActivityInput{Subject: subject}
		}},
		{"confirmation call", func(c *workflowCheckpoint, subject genrunidentity.RunSubject) {
			call := c.Batch.Calls[0]
			call.Subject = subject
			c.Pending[0].Confirmation = &checkpointConfirmation{
				ID: "confirm", Call: call, Title: "Confirm", Prompt: "Proceed?",
				DeniedResult: rawjson.Message(`{"value":"denied"}`),
			}
			c.Pending[0].Await = nil
		}},
	}
	owners := []struct {
		name    string
		subject genrunidentity.RunSubject
	}{
		{"same agent", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("svc.agent")}},
		{"other agent", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("other.agent")}},
		{"service tool", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{
			Registry: "primary", Tool: "svc.lookup",
		})}},
	}
	for _, location := range locations {
		for _, owner := range owners {
			t.Run(location.name+"/"+owner.name, func(t *testing.T) {
				suspension := recordedInputSuspension(t, "svc.lookup")
				rewriteSuspensionCheckpointAndPublic(t, suspension, func(c *workflowCheckpoint) {
					location.set(c, owner.subject)
				})
				_, err := decodeWorkflowCheckpointState(suspension)
				if owner.name == "same agent" {
					assert.NoError(t, err)
				} else {
					assert.ErrorContains(t, err, "subject does not match checkpoint owner")
				}
			})
		}
	}
}

func TestCheckpointSubjectsPreserveEarlierRunAndReconstructedCalls(t *testing.T) {
	definition := testAgentDefinition("svc.agent", "svc.workflow", "test", []tools.ToolSpec{newAnyJSONSpec("svc.lookup")}, nil)
	suspension := recordedInputSuspension(t, "svc.lookup")
	rewriteSuspensionCheckpoint(t, suspension, func(c *workflowCheckpoint) {
		c.Batch.Calls[0].RunID = "earlier-run"
		c.Batch.Result.ToolCalls[0].RunID = "earlier-run"
		c.Batch.Records[0].Call.RunID = "earlier-run"
		c.Batch.Records[0].CallRunID = "earlier-run"
		c.State.ToolOutputs[0].CallRunID = "earlier-run"
	})
	checkpoint, err := decodeWorkflowCheckpoint(suspension, definition)
	require.NoError(t, err)
	assert.Equal(t, "run-1", checkpoint.PreviousRunID)
	assert.Equal(t, "earlier-run", checkpoint.Batch.Records[0].Call.RunID)
	assert.Len(t, checkpoint.State.ToolOutputs, 1, "historical planner outputs have no retained Subject")

	for _, subject := range []genrunidentity.RunSubject{
		{Executor: genrunidentity.NewRunExecutorAgent("other.agent")},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "svc.lookup"})},
	} {
		copy := *suspension
		rewriteSuspensionCheckpoint(t, &copy, func(c *workflowCheckpoint) {
			c.Batch.Calls[0].Subject = subject
			c.Batch.Result.ToolCalls[0].Subject = subject
		})
		_, err := decodeWorkflowCheckpoint(&copy, definition)
		assert.ErrorContains(t, err, "subject does not match checkpoint owner")
	}
}
