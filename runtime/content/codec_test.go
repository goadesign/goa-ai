// These tests follow content from peer JSON through typed values and independent
// copies. Invalid incoming or caller-built values cannot enter saved results.
package content_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/engine/temporal"
	toolcontent "goa.design/goa-ai/runtime/content"
)

const completeContent = `[
 {"type":"text","text":"","annotations":{"audience":["user","assistant"],"priority":1,"lastModified":"2026-10-04T00:00:00Z"},"_meta":{"source":{"id":9007199254740993}}},
 {"type":"image","data":"AQI=","mimeType":"image/png"},
 {"type":"audio","data":"","mimeType":"audio/wav"},
 {"type":"resource_link","name":"guide","title":"Guide","uri":"doc://guide","description":"Read the guide","mimeType":"text/plain","size":42.5,"icons":[{"src":"data:image/png;base64,AQI=","mimeType":"image/png","sizes":["any"],"theme":"dark"}],"annotations":{"priority":1},"_meta":{"extension":true}},
 {"type":"resource","resource":{"uri":"doc://inline","mimeType":"text/plain","text":"","_meta":{"nested":true}},"_meta":{"content":true}},
 {"type":"resource","resource":{"uri":"blob://inline","mimeType":"application/octet-stream","blob":""}}
]`

func TestBlocksPreserveVariantsThroughJSONAndWorkflowEncoding(t *testing.T) {
	t.Parallel()
	var blocks toolcontent.Blocks
	require.NoError(t, json.Unmarshal([]byte(completeContent), &blocks))
	require.Len(t, blocks, 6)
	assert.IsType(t, &toolcontent.TextContent{}, blocks[0])
	assert.IsType(t, &toolcontent.ImageContent{}, blocks[1])
	assert.IsType(t, &toolcontent.AudioContent{}, blocks[2])
	assert.IsType(t, &toolcontent.ResourceLink{}, blocks[3])
	assert.IsType(t, &toolcontent.TextResourceContents{}, blocks[4].(*toolcontent.EmbeddedResource).Resource)
	assert.IsType(t, &toolcontent.BlobResourceContents{}, blocks[5].(*toolcontent.EmbeddedResource).Resource)
	encoded, err := json.Marshal(blocks)
	require.NoError(t, err)
	assert.JSONEq(t, completeContent, string(encoded))
	assert.Contains(t, string(encoded), "9007199254740993")

	// The production converter restores the sequence's concrete variants and
	// the surrounding invocation identity rather than map-shaped content.
	type savedContent struct {
		CallID string             `json:"call_id"`
		Blocks toolcontent.Blocks `json:"blocks"`
	}
	converter := temporal.NewAgentDataConverter()
	payload, err := converter.ToPayload(savedContent{CallID: "call-1", Blocks: blocks})
	require.NoError(t, err)
	var restored savedContent
	require.NoError(t, converter.FromPayload(payload, &restored))
	assert.Equal(t, "call-1", restored.CallID)
	assert.Equal(t, blocks, restored.Blocks)
}

func TestBlocksCloneKeepsIndependentMetadataAndResources(t *testing.T) {
	t.Parallel()
	var blocks toolcontent.Blocks
	require.NoError(t, json.Unmarshal([]byte(completeContent), &blocks))
	copy := blocks.Clone()
	text := copy[0].(*toolcontent.TextContent)
	text.Annotations.Audience[0] = toolcontent.RoleAssistant
	*text.Annotations.Priority = 0
	*text.Annotations.LastModified = "changed"
	text.Meta[0] = '['
	link := copy[3].(*toolcontent.ResourceLink)
	*link.Title = "changed"
	*link.Description = "changed"
	*link.MIMEType = "changed"
	*link.Size = 99
	link.Icons[0].Src = "changed"
	*link.Icons[0].MIMEType = "changed"
	*link.Icons[0].Theme = "changed"
	link.Icons[0].Sizes[0] = "changed"
	link.Meta[0] = '['
	resource := copy[4].(*toolcontent.EmbeddedResource).Resource.(*toolcontent.TextResourceContents)
	resource.Text = "changed"
	*resource.MIMEType = "changed"
	resource.Meta[0] = '['
	copy[5].(*toolcontent.EmbeddedResource).Resource.(*toolcontent.BlobResourceContents).Blob = "AQI="
	encoded, err := json.Marshal(blocks)
	require.NoError(t, err)
	assert.JSONEq(t, completeContent, string(encoded))
}

func TestBlocksRejectInvalidIncomingValuesWithoutReplacingPreviousContent(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		`null`, `{}`, `[null]`,
		`[{"type":"other"}]`,
		`[{"type":"text"}]`,
		`[{"type":"image","data":"AQI="}]`,
		`[{"type":"audio","data":"%%%","mimeType":"audio/wav"}]`,
		`[{"type":"resource_link","name":"guide","uri":"relative"}]`,
		`[{"type":"resource","resource":{"uri":"doc://guide"}}]`,
		`[{"type":"resource","resource":{"uri":"doc://guide","text":"","blob":""}}]`,
		`[{"type":"text","text":"hello","_meta":null}]`,
		`[{"type":"text","text":"hello","annotations":{"audience":["system"]}}]`,
	} {
		t.Run(input, func(t *testing.T) {
			blocks := toolcontent.Blocks{&toolcontent.TextContent{Text: "previous"}}
			require.Error(t, json.Unmarshal([]byte(input), &blocks))
			assert.Equal(t, toolcontent.Blocks{&toolcontent.TextContent{Text: "previous"}}, blocks)
		})
	}
}

func TestBlocksRejectInvalidCallerBuiltValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		block toolcontent.ContentBlock
	}{
		{"nil block", nil},
		{"nil text", (*toolcontent.TextContent)(nil)},
		{"nil resource", &toolcontent.EmbeddedResource{}},
		{"nil text resource", &toolcontent.EmbeddedResource{Resource: (*toolcontent.TextResourceContents)(nil)}},
		{"invalid base64", &toolcontent.ImageContent{Data: "%%%", MIMEType: "image/png"}},
		{"non-finite priority", &toolcontent.TextContent{Annotations: &toolcontent.Annotations{Priority: new(math.NaN())}}},
		{"non-finite size", &toolcontent.ResourceLink{Name: "guide", URI: "doc://guide", Size: new(math.Inf(1))}},
		{"invalid metadata", &toolcontent.TextContent{Meta: json.RawMessage(`[]`)}},
		{"invalid text encoding", &toolcontent.TextContent{Text: string([]byte{0xff})}},
		{"invalid resource text", &toolcontent.EmbeddedResource{Resource: &toolcontent.TextResourceContents{URI: "doc://guide", Text: string([]byte{0xff})}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := json.Marshal(toolcontent.Blocks{test.block})
			assert.Error(t, err)
		})
	}
}

func TestBlocksEncodeEmptySequenceAsArray(t *testing.T) {
	t.Parallel()
	for _, blocks := range []toolcontent.Blocks{nil, {}} {
		encoded, err := json.Marshal(blocks)
		require.NoError(t, err)
		assert.Equal(t, "[]", string(encoded))
	}
}

func TestBlocksWorkflowEncodingUsesExistingOperationBudget(t *testing.T) {
	t.Parallel()
	converter := temporal.NewAgentDataConverter()
	oversized := toolcontent.Blocks{&toolcontent.TextContent{Text: strings.Repeat("x", engine.MaxPayloadBytes+1)}}
	_, err := converter.ToPayload(oversized)
	require.ErrorContains(t, err, "maximum aggregate size")
	// Separate operations each retain their own byte allowance. Content does
	// not turn the existing workflow payload limit into a run-wide limit.
	valid := toolcontent.Blocks{&toolcontent.TextContent{Text: strings.Repeat("x", engine.MaxPayloadBytes/2)}}
	for range 2 {
		_, err := converter.ToPayload(valid)
		require.NoError(t, err)
	}
}

func TestBlocksRejectInvalidIncomingTextEncoding(t *testing.T) {
	t.Parallel()
	input := append([]byte(`[{"type":"text","text":"`), 0xff)
	input = append(input, []byte(`"}]`)...)
	var blocks toolcontent.Blocks
	assert.ErrorContains(t, json.Unmarshal(input, &blocks), "invalid UTF-8")
}
