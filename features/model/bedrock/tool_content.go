// Package bedrock sends supplied tool images and documents inside the matching
// Converse toolResult. Resource descriptions do not cause network reads.
package bedrock

import (
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"goa.design/goa-ai/features/model/internal/toolcontent"
	"goa.design/goa-ai/runtime/agent/model"
	content "goa.design/goa-ai/runtime/content"
)

// encodeToolContent maps the shared text/image/document contract to native
// Converse variants. The API and selected model own model-specific support.
func encodeToolContent(blocks content.Blocks) ([]brtypes.ToolResultContentBlock, error) {
	parts, err := toolcontent.Parts(blocks)
	if err != nil {
		return nil, err
	}
	items := make([]brtypes.ToolResultContentBlock, 0, len(parts))
	for _, part := range parts {
		switch value := part.(type) {
		case model.TextPart:
			items = append(items, &brtypes.ToolResultContentBlockMemberText{Value: value.Text})
		case model.ImagePart:
			items = append(items, &brtypes.ToolResultContentBlockMemberImage{Value: brtypes.ImageBlock{
				Format: brtypes.ImageFormat(value.Format),
				Source: &brtypes.ImageSourceMemberBytes{Value: value.Bytes},
			}})
		case model.DocumentPart:
			items = append(items, &brtypes.ToolResultContentBlockMemberDocument{Value: brtypes.DocumentBlock{
				Name:   aws.String(value.Name),
				Format: brtypes.DocumentFormat(value.Format),
				Source: &brtypes.DocumentSourceMemberBytes{Value: value.Bytes},
			}})
		default:
			return nil, fmt.Errorf("bedrock: unsupported tool content %T: %w", part, model.ErrToolContentUnsupported)
		}
	}
	return items, nil
}
