package hooks

// Saved rejection payloads must preserve old bytes and strictly validate the
// new exact-or-omitted reason contract before exposing an event to subscribers.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestRejectionReasonCodecPreservesLegacyBytes(t *testing.T) {
	digest, size := errorevidence.FingerprintText("historical reason")
	legacy := fmt.Sprintf(`{"ReasonSHA256":%q,"ReasonSize":%d}`, digest, size)
	record := &runlog.ActivityInput{Type: PlannerOutputRejected, RunID: "run", AgentID: "service.agent", Payload: rawjson.Message(legacy)}
	event, err := DecodeFromRecordInput(record)
	require.NoError(t, err)
	encoded, err := EncodeRecordPayload(event)
	require.NoError(t, err)
	assert.Equal(t, legacy, string(encoded))
	assert.Empty(t, event.(*PlannerOutputRejectedEvent).ReasonVersion)
	assert.Empty(t, event.(*PlannerOutputRejectedEvent).Reason)
}

func TestRejectionReasonCodecRequiresExactVersionedText(t *testing.T) {
	for _, reason := range []string{"", "field[7] is invalid", strings.Repeat("é", errorevidence.MaxMessageBytes/2), strings.Repeat("x", errorevidence.MaxMessageBytes+1), string([]byte{0xff})} {
		digest, size := errorevidence.FingerprintText(reason)
		event, err := NewPlannerOutputRejectedEvent("run", "service.agent", "", digest, int64(size))
		require.NoError(t, err)
		event.ReasonVersion = errorevidence.ReasonVersion
		event.Reason, event.ReasonOmitted = errorevidence.RetainedReason(reason)
		encoded, err := EncodeRecordPayload(event)
		require.NoError(t, err)
		record := &runlog.ActivityInput{Type: PlannerOutputRejected, RunID: "run", AgentID: "service.agent", Payload: encoded}
		decoded, err := DecodeFromRecordInput(record)
		require.NoError(t, err)
		reencoded, err := EncodeRecordPayload(decoded)
		require.NoError(t, err)
		assert.Equal(t, encoded, reencoded)
		record.Payload = rawjson.Message(strings.TrimSuffix(string(encoded), "}") + `,"UnknownDiagnostic":true}`)
		_, err = DecodeFromRecordInput(record)
		require.ErrorContains(t, err, "unknown field")
	}
	digest, size := errorevidence.FingerprintText("correct")
	for _, version := range []string{"", "unknown", errorevidence.ReasonVersion} {
		record := &runlog.ActivityInput{Type: PlannerOutputRejected, RunID: "run", AgentID: "service.agent",
			Payload: rawjson.Message(fmt.Sprintf(`{"ReasonVersion":%q,"Reason":"wrong","ReasonSHA256":%q,"ReasonSize":%d}`, version, digest, size))}
		_, err := DecodeFromRecordInput(record)
		assert.Error(t, err)
	}
}

func TestOmittedRejectionEncodeRequiresCanonicalFingerprint(t *testing.T) {
	digest, _ := errorevidence.FingerprintText("reason")
	for _, invalid := range []struct {
		digest string
		size   int64
	}{
		{"bad", errorevidence.MaxMessageBytes + 1},
		{strings.ToUpper(digest), errorevidence.MaxMessageBytes + 1},
		{digest, -1},
	} {
		for _, event := range []Event{
			&PlannerOutputRejectedEvent{ReasonVersion: errorevidence.ReasonVersion, ReasonOmitted: "size_limit", ReasonSHA256: invalid.digest, ReasonSize: invalid.size},
			&ModelOutputRejectedEvent{ReasonVersion: errorevidence.ReasonVersion, ReasonOmitted: "size_limit", ReasonSHA256: invalid.digest, ReasonSize: invalid.size},
		} {
			_, err := EncodeRecordPayload(event)
			require.Error(t, err)
		}
	}
}
