// These tests preserve storage-local additions to an accepted workflow record.
package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestScheduledCallStoragePreservesLargerEnrichedPayload(t *testing.T) {
	toolID := tools.Ident("records.read")
	hint := strings.Repeat("h", 300_000)
	rt := New(newTestStore(), WithHintOverrides(map[tools.Ident]HintOverrideFunc{
		toolID: func(context.Context, tools.Ident, any) (string, bool) { return hint, true },
	}))
	rt.toolSpecs[toolID] = newTypedHintSpec(toolID)
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
	event := hooks.NewToolCallScheduledEvent("run", subject, "session", toolID, "call", rawjson.Message(`{"resolution":""}`), "queue", "parent-call", 2)
	event.ModelToolCallID = "model-call"
	event.ContinuationRootToolCallID = "root-call"
	baseline, err := hooks.EncodeRecordPayload(event)
	require.NoError(t, err)
	event.Payload = rawjson.Message(`{"resolution":"` + strings.Repeat("x", workflowcodec.MaxRecordPayloadBytes-len(baseline)) + `"}`)
	input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{EventKey: "scheduled", TurnID: "turn", TimestampMS: 1234})
	require.NoError(t, err)
	require.Len(t, input.Payload, workflowcodec.MaxRecordPayloadBytes)
	original := *input
	for range 2 {
		_, err := rt.recordResult(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, original, *input)
	}
	page, err := rt.Store.ListRunRecords(t.Context(), input.RunID, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	saved := page.Events[1]
	assert.Greater(t, len(saved.Payload), workflowcodec.MaxRecordPayloadBytes)
	assert.Equal(t, input.EventKey, saved.EventKey)
	assert.Equal(t, input.Subject, saved.Subject)
	assert.Equal(t, input.SessionID, saved.SessionID)
	assert.Equal(t, input.TurnID, saved.TurnID)
	assert.Equal(t, time.UnixMilli(input.TimestampMS).UTC(), saved.Timestamp)
	decoded, err := hooks.DecodeRunlogEvent(saved)
	require.NoError(t, err)
	call, ok := decoded.(*hooks.ToolCallScheduledEvent)
	require.True(t, ok)
	event.DisplayHint = hint
	expected, err := hooks.EncodeRecordPayload(event)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(expected, saved.Payload), "stored payload must retain all call fields and the complete hint")
	assert.Equal(t, event.Payload, call.Payload)
	assert.Equal(t, event.ModelToolCallID, call.ModelToolCallID)
	assert.Equal(t, event.ParentToolCallID, call.ParentToolCallID)
	assert.Equal(t, event.ContinuationRootToolCallID, call.ContinuationRootToolCallID)
	assert.Equal(t, event.ExpectedChildrenTotal, call.ExpectedChildrenTotal)
}
