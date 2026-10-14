// Selected-operation failures retain the former stored event and engine error.
// Only their exact producer may omit duplicate diagnostic text during transport.
package runtime

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/toolerrors"
)

func TestSelectedOperationFailurePreservesOriginalEventAndEngineError(t *testing.T) {
	cause := toolerrors.NewWithCause("selected failure: é\n\"quoted\"", toolerrors.New("inner cause"))
	selected := &selectedOperationError{cause: cause}
	previous := fmt.Errorf("registered tool failed: %w", cause)
	assert.Equal(t, previous.Error(), selected.Error())
	assert.Same(t, cause, errors.Unwrap(selected))
	assert.ErrorIs(t, selected, cause.Cause) //nolint:testifylint // Failure conversion compares constructed errors independently of this cause-chain assertion.
	failureCodec := temporal.GetDefaultFailureConverter()
	assert.True(t, proto.Equal(failureCodec.ErrorToFailure(temporalerrors.Wrap(previous)), failureCodec.ErrorToFailure(temporalerrors.Wrap(selected))))
	for _, test := range []struct {
		name     string
		actual   error
		previous error
		derived  bool
	}{
		{"direct", selected, previous, true},
		{"wrapped", fmt.Errorf("outer context: %w", selected), fmt.Errorf("outer context: %w", previous), false},
		{"joined", errors.Join(selected, errors.New("cleanup failed")), errors.Join(previous, errors.New("cleanup failed")), false},
		{"independent", errors.New("independent failure"), errors.New("independent failure"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "registry", Tool: "records.read"})}
			input := &RunInput{RunID: "selected-run", Subject: subject, SessionID: "session"}
			output := &api.RunOutput{FinalToolResult: &api.ToolEvent{Failure: &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: cause, Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}}}
			metadata := recordDispatchMetadata{EventKey: "terminal-event", TimestampMS: 1_700_000_000_000}
			selectedResult, resultErr := operationResultFromRunOutput(output)
			require.NoError(t, resultErr)
			command, err := buildRunTerminalCommand(input, "turn", runStatusFailed, test.actual, selectedResult, metadata)
			require.NoError(t, err)
			assert.Equal(t, test.derived, command.FailureFromOperation)
			labels := map[string]string{"caller": "saved-owner"}
			stored, event, err := runTerminalStorageCommand(command, session.RunMeta{Labels: labels})
			require.NoError(t, err)
			expected := hooks.NewRunCompletedEvent(input.RunID, subject, input.SessionID, runStatusFailed, run.PhaseFailed, labels, test.previous, nil)
			record, err := prepareHookRecordInputWithMetadata(expected, "turn", metadata)
			require.NoError(t, err)
			assert.Equal(t, []byte(record.Payload), []byte(stored.Record.Payload))
			assert.Equal(t, hooks.RunFailureFromError(test.previous), event.Failure)
			assert.Equal(t, labels, event.Labels)
			value, ok := stored.OperationResult.Outcome.AsFailure()
			require.True(t, ok)
			assert.Equal(t, cause.Message, value.Message)
		})
	}
}

func TestSelectedOperationFailureRejectsContradictoryCommandBeforeWrite(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*api.RunTerminalCommand)
	}{
		{"missing result", func(c *api.RunTerminalCommand) { c.OperationResult = nil }},
		{"malformed result", func(c *api.RunTerminalCommand) { c.OperationResult = []byte(`{}`) }},
		{"independent diagnostic", func(c *api.RunTerminalCommand) {
			c.Record.Payload = []byte(`{"status":"failed","phase":"failed","failure":{"message":"Something went wrong.","debug_message":"independent failure","kind":"internal","retryable":true}}`)
		}},
		{"successful lifecycle", func(c *api.RunTerminalCommand) { c.Record.Payload = []byte(`{"status":"success","phase":"completed"}`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newTestStore()
			subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
			admitRunForTest(t, base, session.RunMeta{Subject: subject, RunID: "terminal-run", Status: session.RunStatusRunning})
			store := &terminalActivityFaultStore{Store: base}
			runtime := New(store)
			cause := toolerrors.New("selected failure")
			output := &api.RunOutput{FinalToolResult: &api.ToolEvent{Failure: &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: cause, Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}}}
			selectedResult, resultErr := operationResultFromRunOutput(output)
			require.NoError(t, resultErr)
			command, err := buildRunTerminalCommand(&RunInput{RunID: "terminal-run", Subject: subject}, "", runStatusFailed, &selectedOperationError{cause: cause}, selectedResult, recordDispatchMetadata{EventKey: "terminal-event", TimestampMS: 1_700_000_000_000})
			require.NoError(t, err)
			test.change(command)
			_, err = runtime.completeRun(t.Context(), command)
			require.Error(t, err)
			assert.Empty(t, store.terminalRecords)
		})
	}
}

func TestSelectedOperationFailureRejectsAnotherSelectedMessage(t *testing.T) {
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
	output := &api.RunOutput{FinalToolResult: &api.ToolEvent{Failure: &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: toolerrors.New("another message"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}}}
	selectedResult, resultErr := operationResultFromRunOutput(output)
	require.NoError(t, resultErr)
	_, err := buildRunTerminalCommand(&RunInput{RunID: "terminal-run", Subject: subject}, "", runStatusFailed, &selectedOperationError{cause: toolerrors.New("original message")}, selectedResult, recordDispatchMetadata{EventKey: "terminal-event", TimestampMS: 1_700_000_000_000})
	require.ErrorContains(t, err, "does not match its frozen result")
}

func TestIndependentTerminalFailureRetainsExistingMessageLimits(t *testing.T) {
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
	input := &RunInput{RunID: "terminal-run", Subject: subject}
	metadata := recordDispatchMetadata{EventKey: "terminal-event", TimestampMS: 1_700_000_000_000}
	_, err := buildRunTerminalCommand(input, "", runStatusFailed, errors.New(strings.Repeat("x", maxHookPayloadBytes)), nil, metadata)
	require.Error(t, err)
	cause := toolerrors.New(strings.Repeat("x", 600_000))
	output := &api.RunOutput{FinalToolResult: &api.ToolEvent{Failure: &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: cause, Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}}}
	for _, independent := range []error{
		errors.New(strings.Repeat("y", 600_000)),
		fmt.Errorf("independent context: %w", &selectedOperationError{cause: cause}),
		errors.Join(&selectedOperationError{cause: cause}, errors.New("cleanup failed")),
	} {
		selectedResult, resultErr := operationResultFromRunOutput(output)
		require.NoError(t, resultErr)
		command, err := buildRunTerminalCommand(input, "", runStatusFailed, independent, selectedResult, metadata)
		require.NoError(t, err)
		assert.False(t, command.FailureFromOperation)
		_, err = workflowcodec.NewDataConverter().ToPayload(&api.StorageActivityCommand{Terminal: command})
		require.ErrorContains(t, err, "maximum aggregate size")
	}
}
