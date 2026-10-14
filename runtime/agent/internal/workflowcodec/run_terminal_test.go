// Terminal commands must survive engine transport without losing original
// ownership or accepting fields discarded by generated union unmarshaling.
package workflowcodec

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/api"
)

func TestUnstartedTerminalActivityRoundTrip(t *testing.T) {
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("executor")}
	origin := genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "root"})}
	terminal := genrunterminal.UnstartedExecutionTerminal{
		RunID: "root", Subject: &subject, Origin: &origin,
		RequestDigest: bytes.Repeat([]byte{1}, 32),
		Time:          genrunterminal.NewExecutionTerminalTimeWorkflowDecision("2026-01-01T00:00:00Z"),
		Outcome:       genrunterminal.NewUnstartedExecutionOutcomeCanceled("user_requested"),
	}
	command := api.StorageActivityCommand{UnstartedTerminal: &api.UnstartedTerminalCommand{
		RunID: terminal.RunID, Subject: subject, RequestDigest: terminal.RequestDigest,
		Time: terminal.Time, Outcome: terminal.Outcome, Root: &struct{}{},
	}}
	codec := NewDataConverter()
	payload, err := codec.ToPayload(command)
	require.NoError(t, err)
	var restored api.StorageActivityCommand
	require.NoError(t, codec.FromPayload(payload, &restored))
	assert.Equal(t, command, restored)
	var budget Budget
	require.NoError(t, budget.AddEncodedSource(command))
	assert.GreaterOrEqual(t, budget.used, len(payload.Data))

	result := api.StorageActivityResult{UnstartedTerminal: &genrunterminal.UnstartedExecutionTerminalResult{
		Result: genrunterminal.NewUnstartedExecutionTerminalDecisionStored(&terminal),
	}}
	payload, err = codec.ToPayload(result)
	require.NoError(t, err)
	var restoredResult api.StorageActivityResult
	require.NoError(t, codec.FromPayload(payload, &restoredResult))
	assert.Equal(t, result, restoredResult)
}

func TestUnstartedTerminalUnionRejectsDiscardedFields(t *testing.T) {
	for _, saved := range []string{
		`{"type":"workflow_decision","value":"2026-01-01T00:00:00Z","unknown":true}`,
		`{"type":"workflow_decision","value":"2026-01-01T00:00:00Z","type":"engine_closure"}`,
	} {
		var command struct {
			Time genrunterminal.ExecutionTerminalTime
		}
		require.Error(t, DecodeJSON([]byte(`{"Time":`+saved+`}`), &command))
	}
	var command struct {
		Outcome genrunterminal.UnstartedExecutionOutcome
	}
	require.Error(t, DecodeJSON([]byte(`{"Outcome":{"type":"failed","value":{"retryable":false,"unknown":true}}}`), &command))
}

func TestUnstartedTerminalStartedMarkerRejectsUnknownFields(t *testing.T) {
	var result struct {
		Decision genrunterminal.UnstartedExecutionTerminalDecision
	}
	require.Error(t, DecodeJSON([]byte(`{"Decision":{"type":"started","value":{"unknown":true}}}`), &result))
	require.NoError(t, DecodeJSON([]byte(`{"Decision":{"type":"started","value":{}}}`), &result))
}

func TestPreparationDecisionActivityRoundTrip(t *testing.T) {
	for _, state := range []genrunterminal.PreparationState{
		genrunterminal.NewPreparationStateProceed(&genrunterminal.PreparationProceed{}),
		genrunterminal.NewPreparationStateStop("custom-stop"),
	} {
		expected := api.StorageActivityResult{PreparationBegin: &genrunterminal.PreparationDecision{State: state}}
		converter := NewDataConverter()
		payload, err := converter.ToPayload(expected)
		require.NoError(t, err)
		var actual api.StorageActivityResult
		require.NoError(t, converter.FromPayload(payload, &actual))
		assert.Equal(t, expected, actual)
		var budget Budget
		require.NoError(t, budget.AddEncodedSource(expected))
		assert.GreaterOrEqual(t, budget.used, len(payload.Data))
	}
}

func TestPreparationDecisionRejectsInvalidSavedBranches(t *testing.T) {
	for _, saved := range []string{
		`{}`, `{"state":{"type":"stop","value":""}}`,
		`{"state":{"type":"proceed","value":{"unknown":true}}}`,
		`{"state":{"type":"stop","value":"custom","unknown":true}}`,
		`{"state":{"type":"stop","value":"custom","type":"proceed"}}`,
	} {
		var decision genrunterminal.PreparationDecision
		assert.Error(t, DecodeJSON([]byte(saved), &decision), saved) //nolint:testifylint // Each saved input is checked independently, so a failure must not skip later inputs.
	}
	invalid := genrunterminal.PreparationDecision{State: genrunterminal.NewPreparationStateStop("")}
	_, err := NewDataConverter().ToPayload(invalid)
	assert.Error(t, err) //nolint:testifylint // The following budget check does not depend on the conversion error.
	var budget Budget
	assert.Error(t, budget.AddEncodedSource(invalid))
}
