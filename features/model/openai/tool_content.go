// Package openai keeps typed content inside its originating function output.
// Supplied images and documents use native API items; no resource URI is opened.
package openai

import (
	"encoding/base64"
	"fmt"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"goa.design/goa-ai/features/model/internal/toolcontent"
	"goa.design/goa-ai/runtime/agent/model"
)

// encodeToolContent preserves the semantic text and appends assistant-visible
// native content in tool order. Plain results use the API's string variant;
// content-bearing results use its typed array variant.
func encodeToolContent(part model.ToolResultPart, text string) (responses.ResponseInputItemFunctionCallOutputOutputUnionParam, error) {
	if len(part.Blocks) == 0 {
		return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt(text)}, nil
	}
	parts, err := toolcontent.Parts(part.Blocks)
	if err != nil {
		return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{}, err
	}
	items := responses.ResponseFunctionCallOutputItemListParam{
		{OfInputText: &responses.ResponseInputTextContentParam{Text: text}},
	}
	for _, part := range parts {
		switch value := part.(type) {
		case model.TextPart:
			items = append(items, responses.ResponseFunctionCallOutputItemUnionParam{
				OfInputText: &responses.ResponseInputTextContentParam{Text: value.Text},
			})
		case model.ImagePart:
			items = append(items, responses.ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &responses.ResponseInputImageContentParam{
					ImageURL: param.NewOpt(fmt.Sprintf("data:image/%s;base64,%s", value.Format, base64.StdEncoding.EncodeToString(value.Bytes))),
					Detail:   responses.ResponseInputImageContentDetailAuto,
				},
			})
		case model.DocumentPart:
			data, err := toolDocumentData(value)
			if err != nil {
				return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{}, err
			}
			items = append(items, responses.ResponseFunctionCallOutputItemUnionParam{
				OfInputFile: &responses.ResponseInputFileContentParam{
					FileData: param.NewOpt(data),
					Filename: param.NewOpt(documentFilename(value)),
				},
			})
		default:
			return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{}, fmt.Errorf("openai: unsupported tool content %T: %w", part, model.ErrToolContentUnsupported)
		}
	}
	return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfResponseFunctionCallOutputItemArray: items}, nil
}

// toolDocumentData emits the MIME-qualified data URL documented for Responses
// file inputs. Only formats represented by the shared document contract enter
// this conversion; unknown formats fail before a provider request is sent.
func toolDocumentData(part model.DocumentPart) (string, error) {
	var mime string
	switch part.Format {
	case model.DocumentFormatPDF:
		mime = "application/pdf"
	case model.DocumentFormatTXT:
		mime = "text/plain"
	case model.DocumentFormatCSV:
		mime = "text/csv"
	case model.DocumentFormatDOC:
		mime = "application/msword"
	case model.DocumentFormatDOCX:
		mime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case model.DocumentFormatXLS:
		mime = "application/vnd.ms-excel"
	case model.DocumentFormatXLSX:
		mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case model.DocumentFormatHTML:
		mime = "text/html"
	case model.DocumentFormatMD:
		mime = "text/markdown"
	default:
		return "", fmt.Errorf("openai: tool document format %q: %w", part.Format, model.ErrToolContentUnsupported)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(part.Bytes), nil
}
