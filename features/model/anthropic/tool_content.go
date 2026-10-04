// Package anthropic nests tool media in the matching tool_result. It uses
// Claude's typed image and document sources and never opens resource addresses.
package anthropic

import (
	"encoding/base64"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"goa.design/goa-ai/features/model/internal/toolcontent"
	"goa.design/goa-ai/runtime/agent/model"
	content "goa.design/goa-ai/runtime/content"
)

// encodeToolContent returns native blocks for assistant-visible tool content.
// Text descriptions remain text, while image and document bytes remain media.
func encodeToolContent(blocks content.Blocks) ([]sdk.ToolResultBlockParamContentUnion, error) {
	parts, err := toolcontent.Parts(blocks)
	if err != nil {
		return nil, err
	}
	items := make([]sdk.ToolResultBlockParamContentUnion, 0, len(parts))
	for _, part := range parts {
		switch value := part.(type) {
		case model.TextPart:
			items = append(items, sdk.ToolResultBlockParamContentUnion{OfText: &sdk.TextBlockParam{Text: value.Text}})
		case model.ImagePart:
			image, err := encodeImage(value, model.ConversationRoleUser)
			if err != nil {
				return nil, err
			}
			items = append(items, sdk.ToolResultBlockParamContentUnion{OfImage: image.OfImage})
		case model.DocumentPart:
			var source sdk.DocumentBlockParamSourceUnion
			switch value.Format {
			case model.DocumentFormatPDF:
				source.OfBase64 = &sdk.Base64PDFSourceParam{Data: base64.StdEncoding.EncodeToString(value.Bytes)}
			case model.DocumentFormatTXT:
				source.OfText = &sdk.PlainTextSourceParam{Data: string(value.Bytes)}
			case model.DocumentFormatCSV, model.DocumentFormatDOC, model.DocumentFormatDOCX,
				model.DocumentFormatXLS, model.DocumentFormatXLSX, model.DocumentFormatHTML,
				model.DocumentFormatMD:
				return nil, fmt.Errorf("anthropic: tool document format %q: %w", value.Format, model.ErrToolContentUnsupported)
			default:
				return nil, fmt.Errorf("anthropic: tool document format %q: %w", value.Format, model.ErrToolContentUnsupported)
			}
			items = append(items, sdk.ToolResultBlockParamContentUnion{OfDocument: &sdk.DocumentBlockParam{Source: source}})
		default:
			return nil, fmt.Errorf("anthropic: unsupported tool content %T: %w", part, model.ErrToolContentUnsupported)
		}
	}
	return items, nil
}
