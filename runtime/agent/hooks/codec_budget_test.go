// These tests distinguish one outbound workflow payload from a larger stored
// record assembled from facts that were accepted separately.
package hooks

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestEncodeToRecordInputPayloadAllowance(t *testing.T) {
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
	for _, prefix := range []string{"", "<\"\n"} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("prefix=%q/delta=%d", prefix, delta), func(t *testing.T) {
				event := NewRunStartedEvent("run", subject, "session", "", "", map[string]string{"saved": prefix})
				baseline, err := EncodeRecordPayload(event)
				require.NoError(t, err)
				event.Labels["saved"] += strings.Repeat("x", workflowcodec.MaxRecordPayloadBytes-len(baseline)+delta)
				payload, err := EncodeRecordPayload(event)
				require.NoError(t, err)
				require.Len(t, payload, workflowcodec.MaxRecordPayloadBytes+delta)
				input, err := EncodeToRecordInput(event, EncodeOptions{EventKey: "event", TurnID: "turn", TimestampMS: 1234})
				if delta > 0 {
					assert.Nil(t, input)
					assert.EqualError(t, err, "runtime: hook payload exceeds budget (1000001 > 1000000 bytes, type=run_started, run_id=run)")
					return
				}
				require.NoError(t, err)
				assert.Equal(t, payload, input.Payload)
				assert.Equal(t, subject, input.Subject)
				assert.Equal(t, "event", input.EventKey)
				assert.Equal(t, "turn", input.TurnID)
				assert.Equal(t, int64(1234), input.TimestampMS)
			})
		}
	}
}

func TestDecodeRecordPreservesLargerHistoricalPayload(t *testing.T) {
	labels := strings.Repeat("x", workflowcodec.MaxRecordPayloadBytes)
	payload := rawjson.Message(`{"labels":{"saved":"` + labels + `"}}`)
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
	input := &runlog.ActivityInput{
		Type: RunStarted, EventKey: "event", RunID: "run", Subject: subject,
		SessionID: "session", TurnID: "turn", TimestampMS: 1234, Payload: payload,
	}
	stored := &runlog.Event{
		Type: input.Type, EventKey: input.EventKey, RunID: input.RunID, Subject: subject,
		SessionID: input.SessionID, TurnID: input.TurnID,
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(), Payload: payload,
	}
	for _, decode := range []func() (Event, error){
		func() (Event, error) { return DecodeFromRecordInput(input) },
		func() (Event, error) { return DecodeRunlogEvent(stored) },
	} {
		event, err := decode()
		require.NoError(t, err)
		started, ok := event.(*RunStartedEvent)
		require.True(t, ok)
		assert.Equal(t, labels, started.Labels["saved"])
		assert.Equal(t, input.EventKey, event.EventKey())
		assert.Equal(t, input.TimestampMS, event.Timestamp())
		assert.Equal(t, input.TurnID, event.TurnID())
		assert.Equal(t, input.Subject, event.Subject())
		encoded, err := EncodeRecordPayload(event)
		require.NoError(t, err)
		assert.Equal(t, payload, encoded)
	}
}
