package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestEncodeTools_NoChoice(t *testing.T) {
	ctx := context.Background()

	cfg, canonToSan, sanToCanon, err := encodeTools(ctx, []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			InputSchema: map[string]any{"type": "object"},
		},
	}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 1)
	require.Nil(t, cfg.ToolChoice)
	require.Len(t, canonToSan, 1)
	require.Len(t, sanToCanon, 1)
}

func TestEncodeTools_ModeAny(t *testing.T) {
	ctx := context.Background()

	cfg, canonToSan, sanToCanon, err := encodeTools(ctx, []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			InputSchema: map[string]any{"type": "object"},
		},
	}, &model.ToolChoice{Mode: model.ToolChoiceModeAny}, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 1)
	require.Len(t, canonToSan, 1)
	require.Len(t, sanToCanon, 1)
	choice, ok := cfg.ToolChoice.(*brtypes.ToolChoiceMemberAny)
	require.True(t, ok, "expected ToolChoiceMemberAny")
	require.NotNil(t, choice)
}

func TestEncodeTools_ModeTool(t *testing.T) {
	ctx := context.Background()

	cfg, canonToSan, sanToCanon, err := encodeTools(ctx, []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			InputSchema: map[string]any{"type": "object"},
		},
	}, &model.ToolChoice{
		Mode: model.ToolChoiceModeTool,
		Name: "lookup",
	}, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 1)
	require.Len(t, canonToSan, 1)
	require.Len(t, sanToCanon, 1)
	member, ok := cfg.ToolChoice.(*brtypes.ToolChoiceMemberTool)
	require.True(t, ok, "expected ToolChoiceMemberTool")
	require.NotNil(t, member)
	require.NotNil(t, member.Value.Name)
	require.Equal(t, "lookup", sanToCanon[*member.Value.Name])
}

func TestEncodeTools_ModeNonePreservesConfig(t *testing.T) {
	ctx := context.Background()

	cfg, canonToSan, sanToCanon, err := encodeTools(ctx, []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			InputSchema: map[string]any{"type": "object"},
		},
	}, &model.ToolChoice{Mode: model.ToolChoiceModeNone}, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 1)
	require.Nil(t, cfg.ToolChoice)
	require.Len(t, canonToSan, 1)
	require.Len(t, sanToCanon, 1)
}

func TestEncodeTools_ChoiceWithoutToolsErrors(t *testing.T) {
	ctx := context.Background()

	_, _, _, err := encodeTools(ctx, nil, &model.ToolChoice{Mode: model.ToolChoiceModeAny}, false)
	require.Error(t, err)
}

func TestEncodeTools_AppendsCacheCheckpoint(t *testing.T) {
	ctx := context.Background()

	cfg, _, _, err := encodeTools(ctx, []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			InputSchema: map[string]any{"type": "object"},
		},
	}, nil, true)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 2, "expected tool spec + cache checkpoint")
	_, ok := cfg.Tools[1].(*brtypes.ToolMemberCachePoint)
	require.True(t, ok, "expected second tool entry to be cache checkpoint")
}

func TestIsNovaModel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty", in: "", want: false},
		{name: "claude", in: "anthropic.claude-3-sonnet-20241022-v1:0", want: false},
		{name: "nova", in: "amazon.nova-pro-v1:0", want: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := isNovaModel(tt.in)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSanitizeToolName_StripsNamespaces(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ada toolset preserves namespace",
			in:   "ada.get_application_status",
			want: "ada_get_application_status",
		},
		{
			name: "ada time series preserves namespace",
			in:   "ada.get_time_series",
			want: "ada_get_time_series",
		},
		{
			name: "chat atlas read subset preserves full canonical id",
			in:   "atlas.read.chat.chat_get_user_details",
			want: "atlas_read_chat_chat_get_user_details",
		},
		{
			name: "chat emit toolset preserves namespace",
			in:   "chat.emit.ask_clarifying_question",
			want: "chat_emit_ask_clarifying_question",
		},
		{
			name: "todos toolset preserves namespace",
			in:   "todos.todos.update_todos",
			want: "todos_todos_update_todos",
		},
		{
			name: "plain name passthrough",
			in:   "lookup",
			want: "lookup",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeToolName(tt.in)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSanitizeToolName_NoCollisionsAcrossToolsets(t *testing.T) {
	a := sanitizeToolName("atlas.read.explain_control_logic")
	b := sanitizeToolName("ada.explain_control_logic")

	require.NotEmpty(t, a)
	require.NotEmpty(t, b)
	require.NotEqual(t, a, b)
}

func TestSanitizeToolName_TruncatesWithStableHashSuffix(t *testing.T) {
	in := "atlas.read.chat." + strings.Repeat("very_long_segment_", 10) + "tool"
	got := sanitizeToolName(in)

	require.NotEmpty(t, got)
	require.LessOrEqual(t, len(got), 64)
	require.Regexp(t, `_[0-9a-f]{8}$`, got)
	require.Equal(t, got, sanitizeToolName(in))
}
