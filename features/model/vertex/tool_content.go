// Package vertex keeps supplied media inside the originating FunctionResponse.
// Its ordered JSON descriptions reference unique native media names, so text
// and media retain their relationship without opening resource addresses.
package vertex

import (
	"fmt"

	"google.golang.org/genai"

	"goa.design/goa-ai/features/model/internal/toolcontent"
	"goa.design/goa-ai/runtime/agent/model"
)

// encodeToolContent preserves the original semantic response and adds ordered
// content descriptions. Native media is referenced once by its unique name,
// following the Gemini 3 function-response contract.
func encodeToolContent(part model.ToolResultPart, semantic map[string]any) (map[string]any, []*genai.FunctionResponsePart, error) {
	if len(part.Blocks) == 0 {
		return semantic, nil, nil
	}
	parts, err := toolcontent.Parts(part.Blocks)
	if err != nil {
		return nil, nil, err
	}
	ordered := make([]any, 0, len(parts))
	var media []*genai.FunctionResponsePart
	for index, part := range parts {
		var mime string
		var bytes []byte
		switch value := part.(type) {
		case model.TextPart:
			ordered = append(ordered, map[string]any{"text": value.Text})
			continue
		case model.ImagePart:
			switch value.Format {
			case model.ImageFormatPNG, model.ImageFormatJPEG, model.ImageFormatWEBP:
				mime, bytes = "image/"+string(value.Format), value.Bytes
			case model.ImageFormatGIF:
				return nil, nil, fmt.Errorf("vertex: tool image format %q: %w", value.Format, model.ErrToolContentUnsupported)
			default:
				return nil, nil, fmt.Errorf("vertex: tool image format %q: %w", value.Format, model.ErrToolContentUnsupported)
			}
		case model.DocumentPart:
			switch value.Format {
			case model.DocumentFormatPDF:
				mime = "application/pdf"
			case model.DocumentFormatTXT:
				mime = "text/plain"
			case model.DocumentFormatCSV, model.DocumentFormatDOC, model.DocumentFormatDOCX,
				model.DocumentFormatXLS, model.DocumentFormatXLSX, model.DocumentFormatHTML,
				model.DocumentFormatMD:
				return nil, nil, fmt.Errorf("vertex: tool document format %q: %w", value.Format, model.ErrToolContentUnsupported)
			default:
				return nil, nil, fmt.Errorf("vertex: tool document format %q: %w", value.Format, model.ErrToolContentUnsupported)
			}
			bytes = value.Bytes
		default:
			return nil, nil, fmt.Errorf("vertex: unsupported tool content %T: %w", part, model.ErrToolContentUnsupported)
		}
		name := fmt.Sprintf("tool-content-%d", index)
		media = append(media, &genai.FunctionResponsePart{InlineData: &genai.FunctionResponseBlob{
			MIMEType:    mime,
			Data:        bytes,
			DisplayName: name,
		}})
		ordered = append(ordered, map[string]any{"$ref": name})
	}
	response := map[string]any{"result": semantic, "content": ordered}
	// A failed tool response keeps its error details at the API's top level,
	// so the provider observes failure even when native media is attached.
	if part.IsError {
		response["error"] = semantic
	}
	return response, media, nil
}
