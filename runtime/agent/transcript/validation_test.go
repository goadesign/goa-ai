package transcript

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestValidatePlannerTranscriptAllowsToolLoopWithoutThinking(t *testing.T) {
	t.Parallel()

	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.TextPart{Text: "calling tool"},
				model.ToolUsePart{ID: "tu1", Name: "search"},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{ToolUseID: "tu1", Content: "ok"},
			},
		},
	}

	require.NoError(t, ValidatePlannerTranscript(msgs))
}

func TestValidatePlannerTranscriptRejectsMissingUserToolResult(t *testing.T) {
	t.Parallel()

	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{ID: "tu1", Name: "search"},
			},
		},
	}

	err := ValidatePlannerTranscript(msgs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "message[0]")
	require.Contains(t, err.Error(), "user tool_result")
}

func TestValidatePlannerTranscriptRejectsPartialToolResults(t *testing.T) {
	t.Parallel()

	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{ID: "tu1", Name: "search"},
				model.ToolUsePart{ID: "tu2", Name: "lookup"},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{ToolUseID: "tu1", Content: "ok"},
			},
		},
	}

	err := ValidatePlannerTranscript(msgs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly 2 tool_result parts")
}

func TestValidatePlannerTranscriptRejectsEmptyToolResultID(t *testing.T) {
	t.Parallel()

	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{ID: "tu1", Name: "search"},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{Content: "ok"},
			},
		},
	}

	err := ValidatePlannerTranscript(msgs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool_result ids must be non-empty and unique")
}

func TestValidatePlannerTranscriptRejectsDuplicateToolUseIDs(t *testing.T) {
	t.Parallel()

	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{ID: "tu1", Name: "search"},
				model.ToolUsePart{ID: "tu1", Name: "lookup"},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{ToolUseID: "tu1", Content: "ok"},
			},
		},
	}

	err := ValidatePlannerTranscript(msgs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool_use ids must be non-empty and unique")
}
