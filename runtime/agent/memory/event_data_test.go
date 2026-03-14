package memory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestToolCallDataRoundTrip(t *testing.T) {
	original := ToolCallData{
		ToolCallID:            "tc-1",
		ParentToolCallID:      "parent-1",
		ToolName:              tools.Ident("svc.tool"),
		PayloadJSON:           rawjson.Message(`{"q":1}`),
		Queue:                 "tools",
		ExpectedChildrenTotal: 2,
	}

	event := NewEvent(time.Unix(10, 0), original, map[string]string{
		"source": "runtime",
	})
	decoded, err := DecodeToolCallData(event)
	require.NoError(t, err)

	assert.Equal(t, original.ToolCallID, decoded.ToolCallID)
	assert.Equal(t, original.ParentToolCallID, decoded.ParentToolCallID)
	assert.Equal(t, original.ToolName, decoded.ToolName)
	assert.Equal(t, string(original.PayloadJSON), string(decoded.PayloadJSON))
	assert.Equal(t, original.Queue, decoded.Queue)
	assert.Equal(t, original.ExpectedChildrenTotal, decoded.ExpectedChildrenTotal)
	assert.Equal(t, map[string]string{"source": "runtime"}, event.Labels)

	input, err := decoded.Input()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"q": float64(1)}, input)
}

func TestToolResultDataRoundTrip(t *testing.T) {
	total := 5
	nextCursor := "next-page"
	original := ToolResultData{
		ToolCallID:       "tc-1",
		ParentToolCallID: "parent-1",
		ToolName:         tools.Ident("svc.tool"),
		ResultJSON:       rawjson.Message(`{"ok":true}`),
		Preview:          "1 row returned",
		Bounds: &agent.Bounds{
			Returned:       1,
			Total:          &total,
			Truncated:      true,
			NextCursor:     &nextCursor,
			RefinementHint: "add a tighter filter",
		},
		Duration:     2 * time.Second,
		ErrorMessage: "",
	}

	decoded, err := DecodeToolResultData(NewEvent(time.Unix(20, 0), original, nil))
	require.NoError(t, err)

	assert.Equal(t, original.ToolCallID, decoded.ToolCallID)
	assert.Equal(t, original.ParentToolCallID, decoded.ParentToolCallID)
	assert.Equal(t, original.ToolName, decoded.ToolName)
	assert.Equal(t, string(original.ResultJSON), string(decoded.ResultJSON))
	assert.Equal(t, original.Preview, decoded.Preview)
	assert.Equal(t, original.Duration, decoded.Duration)
	assert.Equal(t, original.ErrorMessage, decoded.ErrorMessage)
	require.NotNil(t, decoded.Bounds)
	assert.Equal(t, original.Bounds.Returned, decoded.Bounds.Returned)
	assert.Equal(t, *original.Bounds.Total, *decoded.Bounds.Total)
	assert.Equal(t, original.Bounds.Truncated, decoded.Bounds.Truncated)
	assert.Equal(t, *original.Bounds.NextCursor, *decoded.Bounds.NextCursor)
	assert.Equal(t, original.Bounds.RefinementHint, decoded.Bounds.RefinementHint)
}

func TestThinkingDataRoundTrip(t *testing.T) {
	original := ThinkingData{
		Text:         "reasoning",
		Signature:    "sig",
		Redacted:     []byte("opaque"),
		ContentIndex: 3,
		Final:        true,
	}

	decoded, err := DecodeThinkingData(NewEvent(time.Unix(30, 0), original, nil))
	require.NoError(t, err)

	assert.Equal(t, original.Text, decoded.Text)
	assert.Equal(t, original.Signature, decoded.Signature)
	assert.Equal(t, original.Redacted, decoded.Redacted)
	assert.Equal(t, original.ContentIndex, decoded.ContentIndex)
	assert.Equal(t, original.Final, decoded.Final)
}

func TestEventDataCurrentFormatJSONRoundTrip(t *testing.T) {
	total := 5
	nextCursor := "next-page"
	tests := []struct {
		name   string
		event  Event
		assert func(t *testing.T, event Event)
	}{
		{
			name: "tool call",
			event: NewEvent(time.Unix(40, 0), ToolCallData{
				ToolCallID:            "tc-1",
				ParentToolCallID:      "parent-1",
				ToolName:              tools.Ident("svc.tool"),
				PayloadJSON:           rawjson.Message(`{"q":1}`),
				Queue:                 "tools",
				ExpectedChildrenTotal: 2,
			}, nil),
			assert: func(t *testing.T, event Event) {
				decoded, err := DecodeToolCallData(event)
				require.NoError(t, err)
				assert.Equal(t, "tc-1", decoded.ToolCallID)
				assert.Equal(t, "parent-1", decoded.ParentToolCallID)
				assert.Equal(t, tools.Ident("svc.tool"), decoded.ToolName)
				assert.Equal(t, `{"q":1}`, string(decoded.PayloadJSON))
				assert.Equal(t, "tools", decoded.Queue)
				assert.Equal(t, 2, decoded.ExpectedChildrenTotal)
			},
		},
		{
			name: "tool result",
			event: NewEvent(time.Unix(50, 0), ToolResultData{
				ToolCallID:       "tr-1",
				ParentToolCallID: "parent-2",
				ToolName:         tools.Ident("svc.tool"),
				ResultJSON:       rawjson.Message(`{"ok":true}`),
				Preview:          "1 row returned",
				Bounds: &agent.Bounds{
					Returned:       1,
					Total:          &total,
					Truncated:      true,
					NextCursor:     &nextCursor,
					RefinementHint: "add a tighter filter",
				},
				Duration:     2 * time.Second,
				ErrorMessage: "boom",
			}, nil),
			assert: func(t *testing.T, event Event) {
				decoded, err := DecodeToolResultData(event)
				require.NoError(t, err)
				assert.Equal(t, "tr-1", decoded.ToolCallID)
				assert.Equal(t, "parent-2", decoded.ParentToolCallID)
				assert.Equal(t, tools.Ident("svc.tool"), decoded.ToolName)
				assert.Equal(t, `{"ok":true}`, string(decoded.ResultJSON))
				assert.Equal(t, "1 row returned", decoded.Preview)
				require.NotNil(t, decoded.Bounds)
				assert.Equal(t, 1, decoded.Bounds.Returned)
				require.NotNil(t, decoded.Bounds.Total)
				assert.Equal(t, 5, *decoded.Bounds.Total)
				require.NotNil(t, decoded.Bounds.NextCursor)
				assert.Equal(t, "next-page", *decoded.Bounds.NextCursor)
				assert.Equal(t, 2*time.Second, decoded.Duration)
				assert.Equal(t, "boom", decoded.ErrorMessage)
			},
		},
		{
			name: "thinking",
			event: NewEvent(time.Unix(60, 0), ThinkingData{
				Text:         "reasoning",
				Signature:    "sig",
				Redacted:     []byte("opaque"),
				ContentIndex: 3,
				Final:        true,
			}, nil),
			assert: func(t *testing.T, event Event) {
				decoded, err := DecodeThinkingData(event)
				require.NoError(t, err)
				assert.Equal(t, "reasoning", decoded.Text)
				assert.Equal(t, "sig", decoded.Signature)
				assert.Equal(t, []byte("opaque"), decoded.Redacted)
				assert.Equal(t, 3, decoded.ContentIndex)
				assert.True(t, decoded.Final)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.event)
			require.NoError(t, err)

			var roundTripped Event
			require.NoError(t, json.Unmarshal(raw, &roundTripped))

			tc.assert(t, roundTripped)
		})
	}
}

func TestDecodeToolCallDataAcceptsLegacyStructuredPayload(t *testing.T) {
	event := Event{
		Type:      EventToolCall,
		Timestamp: time.Unix(70, 0),
		Data: map[string]any{
			"tool_call_id": "tc-1",
			"tool_name":    "svc.tool",
			"payload":      map[string]any{"q": 1},
		},
	}

	decoded, err := DecodeToolCallData(event)
	require.NoError(t, err)
	input, err := decoded.Input()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"q": float64(1)}, input)
}

func TestDecodeThinkingDataAcceptsLegacyRawBytes(t *testing.T) {
	event := Event{
		Type:      EventThinking,
		Timestamp: time.Unix(80, 0),
		Data: map[string]any{
			"text":          "reasoning",
			"signature":     "sig",
			"redacted":      []byte("opaque"),
			"content_index": 3,
			"final":         true,
		},
	}

	decoded, err := DecodeThinkingData(event)
	require.NoError(t, err)
	assert.Equal(t, []byte("opaque"), decoded.Redacted)
	assert.Equal(t, 3, decoded.ContentIndex)
	assert.True(t, decoded.Final)
}
