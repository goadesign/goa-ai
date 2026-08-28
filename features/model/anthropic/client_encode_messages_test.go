package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestEncodeMessagesProjectsHistoryOnlyToolName(t *testing.T) {
	messages, _, err := encodeMessages([]*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "tu1",
					Name:  "catalog.read.count_events",
					Input: rawjson.Message(`{"from":"2026-02-06T00:00:00Z"}`),
				},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{
					ToolUseID: "tu1",
					Content:   map[string]any{"error": "unknown tool"},
					IsError:   true,
				},
			},
		},
	}, nil, false)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Len(t, messages[0].Content, 1)
	use := messages[0].Content[0].OfToolUse
	require.NotNil(t, use)
	require.Equal(t, toolname.Sanitize("catalog.read.count_events"), use.Name)
	input, err := json.Marshal(use.Input)
	require.NoError(t, err)
	require.JSONEq(t, `{"from":"2026-02-06T00:00:00Z"}`, string(input))
}

func TestEncodeMessagesMapsToolUseIDsBijectively(t *testing.T) {
	messages, _, err := encodeMessages([]*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{ID: "run/turn/call", Name: "lookup", Input: rawjson.Message(`{}`)},
				model.ToolUsePart{ID: "t1", Name: "lookup", Input: rawjson.Message(`{}`)},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{ToolUseID: "run/turn/call", Content: "first"},
				model.ToolResultPart{ToolUseID: "t1", Content: "second"},
			},
		},
	}, nil, false)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Len(t, messages[0].Content, 2)
	require.Len(t, messages[1].Content, 2)

	require.Equal(t, "t2", messages[0].Content[0].OfToolUse.ID)
	require.Equal(t, "t1", messages[0].Content[1].OfToolUse.ID)
	require.Equal(t, "t2", messages[1].Content[0].OfToolResult.ToolUseID)
	require.Equal(t, "t1", messages[1].Content[1].OfToolResult.ToolUseID)
}

func TestEncodeMessagesImages(t *testing.T) {
	imageBytes := []byte("image bytes")
	tests := []struct {
		name      string
		format    model.ImageFormat
		mediaType sdk.Base64ImageSourceMediaType
	}{
		{
			name:      "PNG",
			format:    model.ImageFormatPNG,
			mediaType: sdk.Base64ImageSourceMediaTypeImagePNG,
		},
		{
			name:      "JPEG",
			format:    model.ImageFormatJPEG,
			mediaType: sdk.Base64ImageSourceMediaTypeImageJPEG,
		},
		{
			name:      "GIF",
			format:    model.ImageFormatGIF,
			mediaType: sdk.Base64ImageSourceMediaTypeImageGIF,
		},
		{
			name:      "WebP",
			format:    model.ImageFormatWEBP,
			mediaType: sdk.Base64ImageSourceMediaTypeImageWebP,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages, _, err := encodeMessages([]*model.Message{{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{model.ImagePart{
					Format: test.format,
					Bytes:  imageBytes,
				}},
			}}, nil, false)

			require.NoError(t, err)
			require.Len(t, messages, 1)
			require.Len(t, messages[0].Content, 1)
			image := messages[0].Content[0].OfImage
			require.NotNil(t, image)
			source := image.Source.OfBase64
			require.NotNil(t, source)
			require.Equal(t, test.mediaType, source.MediaType)
			require.Equal(t, base64.StdEncoding.EncodeToString(imageBytes), source.Data)
		})
	}
}

func TestEncodeMessagesPreservesMixedTextImageOrder(t *testing.T) {
	messages, _, err := encodeMessages([]*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{
			model.TextPart{Text: "front view"},
			model.ImagePart{Format: model.ImageFormatPNG, Bytes: []byte("front")},
			model.TextPart{Text: "side view"},
			model.ImagePart{Format: model.ImageFormatJPEG, Bytes: []byte("side")},
		},
	}}, nil, false)

	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 4)
	require.NotNil(t, messages[0].Content[0].OfText)
	require.NotNil(t, messages[0].Content[1].OfImage)
	require.NotNil(t, messages[0].Content[1].OfImage.Source.OfBase64)
	require.NotNil(t, messages[0].Content[2].OfText)
	require.NotNil(t, messages[0].Content[3].OfImage)
	require.NotNil(t, messages[0].Content[3].OfImage.Source.OfBase64)
	require.Equal(t, "front view", messages[0].Content[0].OfText.Text)
	require.Equal(
		t,
		base64.StdEncoding.EncodeToString([]byte("front")),
		messages[0].Content[1].OfImage.Source.OfBase64.Data,
	)
	require.Equal(t, "side view", messages[0].Content[2].OfText.Text)
	require.Equal(
		t,
		base64.StdEncoding.EncodeToString([]byte("side")),
		messages[0].Content[3].OfImage.Source.OfBase64.Data,
	)
}

func TestEncodeMessagesRejectsInvalidImages(t *testing.T) {
	tests := []struct {
		name    string
		role    model.ConversationRole
		format  model.ImageFormat
		wantErr string
	}{
		{
			name:    "system role",
			role:    model.ConversationRoleSystem,
			format:  model.ImageFormatPNG,
			wantErr: "anthropic: image parts are only supported in user messages (role=system)",
		},
		{
			name:    "assistant role",
			role:    model.ConversationRoleAssistant,
			format:  model.ImageFormatPNG,
			wantErr: "anthropic: image parts are only supported in user messages (role=assistant)",
		},
		{
			name:    "unsupported format",
			role:    model.ConversationRoleUser,
			format:  model.ImageFormat("bmp"),
			wantErr: `anthropic: unsupported image format "bmp"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := encodeMessages([]*model.Message{{
				Role: test.role,
				Parts: []model.Part{model.ImagePart{
					Format: test.format,
					Bytes:  []byte("image bytes"),
				}},
			}}, nil, false)

			require.EqualError(t, err, test.wantErr)
		})
	}
}

func TestEncodeMessagesThinkingVariants(t *testing.T) {
	tests := []struct {
		name    string
		part    model.ThinkingPart
		wantErr string
	}{
		{
			name: "signed plaintext",
			part: model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
		},
		{
			name: "redacted",
			part: model.ThinkingPart{Redacted: []byte("opaque"), Final: true},
		},
		{
			name:    "missing signature",
			part:    model.ThinkingPart{Text: "reasoning", Final: true},
			wantErr: "anthropic: thinking part must contain exactly signed content or redacted content",
		},
		{
			name: "mixed variants",
			part: model.ThinkingPart{
				Text:      "reasoning",
				Signature: "sig",
				Redacted:  []byte("opaque"),
				Final:     true,
			},
			wantErr: "anthropic: thinking part must contain exactly signed content or redacted content",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := encodeMessages([]*model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{test.part},
			}}, nil, false)

			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
