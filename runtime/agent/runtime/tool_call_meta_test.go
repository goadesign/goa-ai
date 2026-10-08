// These tests verify that shared tool metadata keeps the runtime API and saved
// JSON records compatible when its definition moves to the tools package.
package runtime_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
)

// TestToolCallMetaPreservesSavedJSON passes the shared type through the existing
// runtime name and checks the JSON shape used by saved execution records.
func TestToolCallMetaPreservesSavedJSON(t *testing.T) {
	tests := []struct {
		name string
		meta tools.ToolCallMeta
		want string
	}{
		{
			name: "empty identifiers",
			want: `{"RunID":"","SessionID":"","TurnID":"","ToolCallID":"","ParentToolCallID":"","Labels":null}`,
		},
		{
			name: "call identifiers and labels",
			meta: tools.ToolCallMeta{
				RunID:            "run-1",
				SessionID:        "session-1",
				TurnID:           "turn-1",
				ToolCallID:       "call-1",
				ParentToolCallID: "parent-1",
				Labels:           map[string]string{"tenant_id": "tenant-1"},
			},
			want: `{"RunID":"run-1","SessionID":"session-1","TurnID":"turn-1","ToolCallID":"call-1","ParentToolCallID":"parent-1","Labels":{"tenant_id":"tenant-1"}}`,
		},
		{
			name: "text only with empty labels",
			meta: tools.ToolCallMeta{TextOnly: true, Labels: map[string]string{}},
			want: `{"TextOnly":true,"RunID":"","SessionID":"","TurnID":"","ToolCallID":"","ParentToolCallID":"","Labels":{}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.meta)
			require.NoError(t, err)
			assert.JSONEq(t, test.want, string(encoded))

			var restored runtime.ToolCallMeta
			require.NoError(t, json.Unmarshal([]byte(test.want), &restored))
			assert.Equal(t, test.meta, restored)
		})
	}
}
