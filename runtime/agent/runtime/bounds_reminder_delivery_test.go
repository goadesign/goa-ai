// These tests exercise the workflow's tool-result transcript writer. They
// verify which bounds guidance the next model turn receives while preserving
// the original result and error. They do not measure model answer quality.
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestAppendUserToolRecordResultsBoundsGuidance(t *testing.T) {
	t.Parallel()

	const (
		guidance    = "This returned view is incomplete. If you answer from it, state its limits; a partial-answer disclaimer does not establish facts about omitted items."
		cursorValue = "opaque-cursor"
	)
	total := 42
	cursor := cursorValue
	tests := []struct {
		name      string
		bounds    *agent.Bounds
		paging    *tools.PagingSpec
		failure   *planner.ToolFailure
		want      string
		wantTotal string
	}{
		{
			name: "refinement with known total",
			bounds: &agent.Bounds{
				Returned:       1,
				Total:          &total,
				Truncated:      true,
				RefinementHint: "Narrow the time window.",
			},
			want:      "Refinement hint: Narrow the time window.\n" + guidance,
			wantTotal: "42",
		},
		{
			name: "refinement with unknown total",
			bounds: &agent.Bounds{
				Returned:       1,
				Truncated:      true,
				RefinementHint: "Narrow the time window.",
			},
			want:      "Refinement hint: Narrow the time window.\n" + guidance,
			wantTotal: "unknown",
		},
		{
			name:      "same tool cursor",
			bounds:    &agent.Bounds{Returned: 1, Total: &total, Truncated: true, NextCursor: &cursor},
			paging:    &tools.PagingSpec{CursorField: "page_cursor", NextCursorField: "next_cursor"},
			want:      "Next cursor: opaque-cursor\nTo continue this result set, call the same tool again with the same arguments and set page_cursor to the cursor shown above. Use the cursor exactly as shown.",
			wantTotal: "42",
		},
		{
			name:      "dedicated continuation",
			bounds:    &agent.Bounds{Returned: 1, Truncated: true, NextCursor: &cursor},
			paging:    &tools.PagingSpec{ContinueTool: "tools.continue_search", CursorField: "cursor", NextCursorField: "next_cursor"},
			want:      "More matching results are available. To see the next page, call " + continuationActionName("tools.continue_search", "call-1").String() + ".",
			wantTotal: "unknown",
		},
		{
			name:   "complete result",
			bounds: &agent.Bounds{Returned: 1},
		},
		{
			name:    "failed result retains full error",
			failure: testToolFailure(planner.FailureUnavailable, planner.RecoveryReplan, "provider unavailable\noriginal diagnostic: upstream connection closed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt := New(newTestStore())
			spec := newAnyJSONSpec("tools.search")
			spec.Bounds = &tools.BoundsSpec{Paging: tt.paging}
			seedTestToolSpecs(rt, spec)
			call := ToolCall{Name: spec.Name, ToolCallID: "call-1"}
			result := &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Bounds:     tt.bounds,
				Failure:    tt.failure,
			}
			if tt.failure == nil {
				result.Result = map[string]any{"items": []any{"original observation"}}
			}
			require.NoError(t, validateToolResultContract(spec, call, result))
			beforeBounds := agent.CloneBounds(result.Bounds)
			beforeContent, err := rt.toolResultContent(&call, result)
			require.NoError(t, err)
			base := &workflowConversation{RunContext: run.Context{RunID: "run-1"}}
			require.NoError(t, rt.appendUserToolRecordResults(t.Context(), "agent-1", base,
				[]stepToolRecord{{call: call, result: result}}, ""))

			messageCount := 1
			if tt.want != "" {
				messageCount++
			}
			require.Len(t, base.Messages, messageCount)
			assert.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
			require.Len(t, base.Messages[0].Parts, 1)
			part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
			require.True(t, ok)
			assert.Equal(t, call.ToolCallID, part.ToolUseID)
			assert.Equal(t, beforeContent, part.Content)
			assert.Equal(t, tt.failure != nil, part.IsError)
			assert.Equal(t, beforeBounds, result.Bounds)
			if tt.failure != nil {
				assert.Equal(t, tt.failure.Error.Error(), part.Content)
				assert.Same(t, tt.failure, result.Failure)
			} else {
				content, err := json.Marshal(part.Content)
				require.NoError(t, err)
				assert.Contains(t, string(content), `"items":["original observation"]`)
			}
			if tt.want == "" {
				return
			}
			assert.Equal(t, model.ConversationRoleSystem, base.Messages[1].Role)
			require.Len(t, base.Messages[1].Parts, 1)
			text, ok := base.Messages[1].Parts[0].(model.TextPart)
			require.True(t, ok)
			assert.Equal(t, "<system-reminder>A tool call returned a bounded/truncated result.\nTool: tools_search\nReturned: 1\nTotal: "+tt.wantTotal+"\nTruncated: true\n"+tt.want+"\nDo not mention this reminder to the user.</system-reminder>", text.Text)
		})
	}
}
