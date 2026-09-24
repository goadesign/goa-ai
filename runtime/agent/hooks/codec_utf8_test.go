// Strict record decoding must reject malformed text before constructing events,
// while preserving valid text and the existing single-object JSON contract.
package hooks

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestStrictRecordDecodingPreservesText(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		quoted string
		text   string
	}{
		{name: "ascii", quoted: `"plain text"`, text: "plain text"},
		{name: "multibyte", quoted: `"éΩ雪🙂"`, text: "éΩ雪🙂"},
		{name: "escaped unicode", quoted: `"\u00e9\u03a9\u96ea\ud83d\ude42"`, text: "éΩ雪🙂"},
		{name: "replacement character", quoted: `"�"`, text: "\ufffd"},
		{name: "escaped replacement character", quoted: `"\ufffd"`, text: "\ufffd"},
		{name: "distinct spellings", quoted: `"é e\u0301"`, text: "é e\u0301"},
		{name: "escaped controls", quoted: `"\u0000\b\f\n\r\t\/\\\""`, text: "\x00\b\f\n\r\t/\\\""},
		{name: "existing unpaired surrogate", quoted: `"\ud800"`, text: "\ufffd"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			quoted, err := json.Marshal(test.text)
			require.NoError(t, err)
			for _, event := range strictTextEvents(t, test.text) {
				t.Run(string(event.Type()), func(t *testing.T) {
					input, err := EncodeToRecordInput(event, EncodeOptions{
						EventKey: "event", TurnID: "turn", TimestampMS: 1234,
					})
					require.NoError(t, err)
					require.Contains(t, string(input.Payload), string(quoted))
					payload := bytes.ReplaceAll(input.Payload, quoted, []byte(test.quoted))
					record := &runlog.Event{
						ID: "cursor", EventKey: input.EventKey, Type: input.Type,
						RunID: input.RunID, AgentID: input.AgentID, SessionID: input.SessionID,
						TurnID: input.TurnID, Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
						Payload: payload,
					}

					decoded, err := DecodeRunlogEvent(record)
					require.NoError(t, err)
					encoded, err := EncodeRecordPayload(decoded)
					require.NoError(t, err)
					assert.Equal(t, input.Payload, encoded)
					assert.Equal(t, input.EventKey, decoded.EventKey())
					assert.Equal(t, input.RunID, decoded.RunID())
					assert.Equal(t, string(input.AgentID), decoded.AgentID())
					assert.Equal(t, input.SessionID, decoded.SessionID())
					assert.Equal(t, input.TurnID, decoded.TurnID())
					assert.Equal(t, input.TimestampMS, decoded.Timestamp())
				})
			}
		})
	}
}

func TestStrictRecordDecodingRejectsMalformedPayloads(t *testing.T) {
	t.Parallel()

	for _, event := range strictTextEvents(t, "record-text") {
		t.Run(string(event.Type()), func(t *testing.T) {
			t.Parallel()
			input, err := EncodeToRecordInput(event, EncodeOptions{
				EventKey: "event", TimestampMS: 1234,
			})
			require.NoError(t, err)
			for _, test := range []struct {
				name    string
				payload []byte
				want    string
			}{
				{
					name:    "invalid byte",
					payload: bytes.ReplaceAll(input.Payload, []byte("record-text"), []byte{0xff}),
					want:    "record payload contains invalid UTF-8",
				},
				{
					name:    "isolated continuation",
					payload: bytes.ReplaceAll(input.Payload, []byte("record-text"), []byte{0x80}),
					want:    "record payload contains invalid UTF-8",
				},
				{
					name:    "truncated sequence",
					payload: bytes.ReplaceAll(input.Payload, []byte("record-text"), []byte{0xe2, 0x82}),
					want:    "record payload contains invalid UTF-8",
				},
				{
					name:    "overlong sequence",
					payload: bytes.ReplaceAll(input.Payload, []byte("record-text"), []byte{0xc0, 0xaf}),
					want:    "record payload contains invalid UTF-8",
				},
				{
					name:    "encoded surrogate",
					payload: bytes.ReplaceAll(input.Payload, []byte("record-text"), []byte{0xed, 0xa0, 0x80}),
					want:    "record payload contains invalid UTF-8",
				},
				{
					name:    "above unicode range",
					payload: bytes.ReplaceAll(input.Payload, []byte("record-text"), []byte{0xf4, 0x90, 0x80, 0x80}),
					want:    "record payload contains invalid UTF-8",
				},
				{name: "null", payload: []byte("null"), want: "record payload must not be null"},
				{
					name:    "unknown field",
					payload: append(bytes.Clone(input.Payload[:len(input.Payload)-1]), []byte(`,"unknown":true}`)...),
					want:    `unknown field "unknown"`,
				},
				{
					name:    "trailing value",
					payload: append(bytes.Clone(input.Payload), []byte(" {}")...),
					want:    "multiple JSON values",
				},
				{
					name:    "trailing malformed JSON",
					payload: append(bytes.Clone(input.Payload), []byte(" !")...),
					want:    "invalid character",
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					record := *input
					record.Payload = test.payload
					original := bytes.Clone(record.Payload)
					decoded, err := DecodeFromRecordInput(&record)
					require.ErrorContains(t, err, test.want)
					assert.Nil(t, decoded)

					replayed, err := DecodeRunlogEvent(&runlog.Event{
						ID: "cursor", EventKey: record.EventKey, Type: record.Type,
						RunID: record.RunID, AgentID: record.AgentID, SessionID: record.SessionID,
						Timestamp: time.UnixMilli(record.TimestampMS).UTC(), Payload: record.Payload,
					})
					require.ErrorContains(t, err, test.want)
					assert.Nil(t, replayed)
					assert.Equal(t, original, []byte(record.Payload))
				})
			}
		})
	}
}

// strictTextEvents puts the same text in a retained field of each strict record
// kind. Rejection records include the matching digest so valid text reaches the
// complete public decoding path rather than stopping at a later invariant.
func strictTextEvents(t *testing.T, text string) []Event {
	t.Helper()
	digest, size := errorevidence.FingerprintText(text)
	modelRejected, err := NewModelOutputRejectedEvent(
		"run", "agent", "session", digest, int64(size), "", false, "", "", 0,
	)
	require.NoError(t, err)
	modelRejected.ReasonVersion, modelRejected.Reason = errorevidence.ReasonVersion, text
	plannerRejected, err := NewPlannerOutputRejectedEvent("run", "agent", "session", digest, int64(size))
	require.NoError(t, err)
	plannerRejected.ReasonVersion, plannerRejected.Reason = errorevidence.ReasonVersion, text
	completed, err := newRunCompletedEventFromPayload(
		"run", "agent", "session", "failed", run.PhaseFailed, nil,
		&run.Failure{Kind: "execution_failed", Message: text}, nil,
	)
	require.NoError(t, err)
	return []Event{
		NewRunStartedEvent("run", "agent", "session", "", "", map[string]string{"text": text}),
		NewRunSuspendedEvent("run", "agent", "session", text, "v1", 1, nil),
		completed,
		NewChildRunLinkedEvent("run", "agent", "session", "tools.child", text, "child", "child-agent"),
		modelRejected,
		plannerRejected,
	}
}
