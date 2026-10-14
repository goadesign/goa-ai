// These tests verify that hook records keep the exact generated run owner and
// reject malformed ownership before replay delivers an event to a subscriber.
package hooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestServiceSubjectLifecycleRoundTrip(t *testing.T) {
	t.Parallel()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(
		&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"},
	)}
	events := []Event{
		NewRunStartedEvent("run", subject, "session", "", "", nil),
		NewRunPhaseChangedEvent("run", subject, "session", run.PhaseExecutingTools),
		NewRunCompletedEvent("run", subject, "session", "success", run.PhaseCompleted, nil, nil, nil),
		NewToolCallScheduledEvent("run", subject, "session", "records.lookup", "call", nil, "", "", 0),
		NewToolResultReceivedEvent("run", subject, "session", "run", "records.lookup", "call", "", nil, nil, nil, "", nil, 0, nil, nil),
	}
	for _, event := range events {
		t.Run(string(event.Type()), func(t *testing.T) {
			record, err := EncodeToRecordInput(event, EncodeOptions{EventKey: "event", TimestampMS: 1})
			require.NoError(t, err)
			assert.Equal(t, subject, record.Subject)
			decoded, err := DecodeFromRecordInput(record)
			require.NoError(t, err)
			assert.Equal(t, subject, decoded.Subject())
			assert.Equal(t, event.Type(), decoded.Type())
		})
	}
}

func TestHookSubjectOwnsServiceFields(t *testing.T) {
	t.Parallel()
	service := &genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"}
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(service)}
	event := NewRunStartedEvent("run", subject, "session", "", "", nil)
	service.Registry = "other"
	first, ok := event.Subject().Executor.AsServiceTool()
	require.True(t, ok)
	assert.Equal(t, "primary", first.Registry)
	first.Tool = "records.changed"
	second, ok := event.Subject().Executor.AsServiceTool()
	require.True(t, ok)
	assert.Equal(t, "records.lookup", second.Tool)

	record, err := EncodeToRecordInput(event, EncodeOptions{EventKey: "event", TimestampMS: 1})
	require.NoError(t, err)
	decoded, err := DecodeFromRecordInput(record)
	require.NoError(t, err)
	saved, ok := record.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	saved.Registry = "changed-after-decode"
	assert.Equal(t, event.Subject(), decoded.Subject())
}

func TestHookCodecRejectsIncompleteSubject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		subject genrunidentity.RunSubject
	}{
		{name: "no executor"},
		{name: "empty Agent", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("")}},
		{name: "nil service", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(nil)}},
		{name: "missing registry", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Tool: "records.lookup"})}},
		{name: "unqualified tool", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "lookup"})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := NewRunStartedEvent("run", test.subject, "session", "", "", nil)
			_, err := EncodeToRecordInput(event, EncodeOptions{EventKey: "event", TimestampMS: 1})
			require.ErrorContains(t, err, "encode hook record subject")
			_, err = DecodeFromRecordInput(&runlog.ActivityInput{Type: RunStarted, Subject: test.subject, Payload: []byte(`{}`)})
			assert.ErrorContains(t, err, "decode hook record subject")
		})
	}
}

func TestHookCodecRejectsServiceSubjectForAgentEvents(t *testing.T) {
	t.Parallel()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(
		&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"},
	)}
	for _, eventType := range []EventType{
		PromptRendered, PolicyDecision, Usage, ModelOutputRejected, PlannerOutputRejected,
		PlannerNote, ThinkingBlock, AssistantMessage, AssistantTurnCommitted, MemoryAppended,
	} {
		t.Run(string(eventType), func(t *testing.T) {
			_, err := DecodeFromRecordInput(&runlog.ActivityInput{Type: eventType, Subject: subject, Payload: []byte(`{}`)})
			assert.ErrorContains(t, err, "event requires an Agent subject")
		})
	}
}

func TestToolResultCorrelationComparesServiceValues(t *testing.T) {
	t.Parallel()
	scheduledSubject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(
		&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"},
	)}
	resultSubject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(
		&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"},
	)}
	scheduled := NewToolCallScheduledEvent("run", scheduledSubject, "session", "records.lookup", "call", nil, "", "", 0)
	result := NewToolResultReceivedEvent("run", resultSubject, "session", "run", "records.lookup", "call", "", nil, nil, nil, "", nil, 0, nil, nil)
	require.NoError(t, ValidateToolResultCorrelation(scheduled, result))
	result.subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(
		&genrunidentity.ServiceToolSubject{Registry: "other", Tool: "records.lookup"},
	)}
	assert.ErrorContains(t, ValidateToolResultCorrelation(scheduled, result), "scheduled subject")
}
