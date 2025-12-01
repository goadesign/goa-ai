package bedrock

import (
	"context"
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
			name: "ada toolset",
			in:   "ada.get_application_status",
			want: "get_application_status",
		},
		{
			name: "ada time series",
			in:   "ada.get_time_series",
			want: "get_time_series",
		},
		{
			name: "chat atlas read subset",
			in:   "atlas.read.chat.chat_get_user_details",
			want: "get_user_details",
		},
		{
			name: "chat emit toolset",
			in:   "chat.emit.ask_clarifying_question",
			want: "ask_clarifying_question",
		},
		{
			name: "todos toolset",
			in:   "todos.todos.update_todos",
			want: "update_todos",
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
