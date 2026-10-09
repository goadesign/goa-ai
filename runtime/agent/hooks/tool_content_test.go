// These tests preserve every content variant in saved tool-result events. Replay
// returns independent values for the same invocation, including tool failures.
package hooks

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/content"
)

func TestToolResultRecordRetainsContentOnSuccessAndFailure(t *testing.T) {
	var blocks content.Blocks
	require.NoError(t, json.Unmarshal([]byte(`[
		{"type":"text","text":"complete","_meta":{"sequence":9007199254740993},"annotations":{"audience":["user","assistant"],"priority":0.25}},
		{"type":"image","data":"AQID","mimeType":"image/png"},
		{"type":"audio","data":"BAUG","mimeType":"audio/wav"},
		{"type":"resource_link","name":"report","uri":"https://example.com/report","icons":[{"src":"https://example.com/icon","sizes":["any"]}]},
		{"type":"resource","resource":{"uri":"report://inline","text":"accepted"}}
	]`), &blocks))
	for _, failed := range []bool{false, true} {
		result := rawjson.Message(`{"value":"accepted"}`)
		var failure *planner.ToolFailure
		if failed {
			result = nil
			failure = &planner.ToolFailure{Kind: planner.FailureDomainRejection, Error: planner.NewToolError("rejected"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
		}
		event := NewToolResultReceivedEvent("result-run", "service.agent", "session-1", "call-run", "remote.lookup", "call-1", "", result, nil, blocks, "preview", nil, 0, nil, failure)
		record, err := EncodeToRecordInput(event, EncodeOptions{EventKey: "content-result", TimestampMS: 1})
		require.NoError(t, err)
		decoded, err := DecodeFromRecordInput(record)
		require.NoError(t, err)
		got := decoded.(*ToolResultReceivedEvent)
		assert.Equal(t, blocks, got.Blocks)
		assert.Equal(t, failed, got.Failure != nil)
		assert.Contains(t, string(record.Payload), "9007199254740993")
		got.Blocks[0].(*content.TextContent).Text = "changed"
		assert.Equal(t, "complete", event.Blocks[0].(*content.TextContent).Text)
		assert.Equal(t, "complete", blocks[0].(*content.TextContent).Text)
	}
}
