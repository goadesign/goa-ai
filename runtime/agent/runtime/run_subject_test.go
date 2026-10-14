package runtime

// Common tool events carry either executor kind. Tool spans must retain the
// conversation and tool identity without inventing Agent attributes for services.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestToolSpanPreservesRunSubjectMeaning(t *testing.T) {
	for _, test := range []struct {
		name    string
		subject genrunidentity.RunSubject
		agentID string
	}{
		{name: "Agent", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("catalog.agent")}, agentID: "catalog.agent"},
		{name: "service", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracer := &toolDiagnosticTracer{}
			runtime := New(newTestStore(), WithTracer(tracer))
			event := hooks.NewToolResultReceivedEvent("run", test.subject, "session", "run", "catalog.lookup", "call", "", nil, nil, nil, "", nil, time.Millisecond, nil, nil)
			require.NoError(t, runtime.recordGenAITelemetryEvent(t.Context(), event))
			require.Len(t, tracer.spans, 1)
			attrs := attrsByKey(tracer.spans[0].attrs)
			assert.Equal(t, "session", attrs[telemetry.AttrGenAIConversationID].AsString())
			assert.Equal(t, "catalog.lookup", attrs[telemetry.AttrGenAIToolName].AsString())
			assert.Equal(t, "call", attrs[telemetry.AttrGenAIToolCallID].AsString())
			if test.agentID == "" {
				assert.NotContains(t, attrs, telemetry.AttrGenAIAgentID)
				assert.NotContains(t, attrs, telemetry.AttrGenAIAgentName)
			} else {
				assert.Equal(t, test.agentID, attrs[telemetry.AttrGenAIAgentID].AsString())
				assert.Equal(t, test.agentID, attrs[telemetry.AttrGenAIAgentName].AsString())
			}
		})
	}
}

func TestCloneToolCallOwnsServiceSubject(t *testing.T) {
	original := ToolCall{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}}
	copied := cloneToolCall(original)
	service, ok := copied.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	service.Registry = "other"
	saved, ok := original.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	assert.Equal(t, "primary", saved.Registry)
}
