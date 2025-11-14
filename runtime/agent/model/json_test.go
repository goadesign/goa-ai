package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPartMarshalJSONIncludesKind(t *testing.T) {
	cases := []struct {
		name string
		part Part
		kind string
	}{
		{name: "thinking", part: ThinkingPart{Text: "think"}, kind: "thinking"},
		{name: "text", part: TextPart{Text: "hello"}, kind: "text"},
		{name: "tool_use", part: ToolUsePart{Name: "search", Input: map[string]any{"q": "golang"}}, kind: "tool_use"},
		{name: "tool_result", part: ToolResultPart{ToolUseID: "tu", Content: map[string]any{"hits": 1}}, kind: "tool_result"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.part)
			require.NoError(t, err)
			var obj map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &obj))

			var kind string
			require.NoError(t, json.Unmarshal(obj["Kind"], &kind))
			require.Equal(t, tt.kind, kind)
		})
	}
}

func TestDecodeMessagePartHonorsKind(t *testing.T) {
	const payload = `{"Kind":"tool_use","Name":"legacy","Args":{"q":"old"}}`
	part, err := decodeMessagePart([]byte(payload))
	require.NoError(t, err)

	tu, ok := part.(ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "legacy", tu.Name)
	require.Equal(t, map[string]any{"q": "old"}, tu.Input)
}
