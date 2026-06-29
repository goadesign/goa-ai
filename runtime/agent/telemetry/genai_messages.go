package telemetry

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"goa.design/goa-ai/runtime/agent/model"
)

type genAIInputMessage struct {
	Role  model.ConversationRole `json:"role"`
	Parts []any                  `json:"parts"`
}

type genAIOutputMessage struct {
	Role         model.ConversationRole `json:"role"`
	Parts        []any                  `json:"parts"`
	FinishReason string                 `json:"finish_reason"`
}

type genAITextPart struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type genAIToolCallPart struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments,omitempty"`
}

type genAIToolCallResponsePart struct {
	Type     string `json:"type"`
	ID       string `json:"id,omitempty"`
	Response any    `json:"response"`
}

type genAIReasoningPart struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type genAIBlobPart struct {
	Type     string `json:"type"`
	Modality string `json:"modality,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Content  string `json:"content"`
}

type genAIURIPart struct {
	Type     string `json:"type"`
	Modality string `json:"modality,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	URI      string `json:"uri"`
}

// GenAIInputMessagesAttr serializes input messages to the OpenTelemetry GenAI
// message schema as a JSON string attribute. Message content can be sensitive;
// callers must opt in explicitly and should avoid enabling this by default.
func GenAIInputMessagesAttr(messages []*model.Message) (attribute.KeyValue, bool, error) {
	if len(messages) == 0 {
		return attribute.KeyValue{}, false, nil
	}
	out := make([]genAIInputMessage, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		out = append(out, genAIInputMessage{
			Role:  msg.Role,
			Parts: genAIParts(msg.Parts),
		})
	}
	if len(out) == 0 {
		return attribute.KeyValue{}, false, nil
	}
	attr, err := genAIMessagesAttr(AttrGenAIInputMessages, out)
	if err != nil {
		return attribute.KeyValue{}, false, err
	}
	return attr, true, nil
}

// GenAIOutputMessagesAttr serializes output messages to the OpenTelemetry GenAI
// message schema as a JSON string attribute. Message content can be sensitive;
// callers must opt in explicitly and should avoid enabling this by default.
func GenAIOutputMessagesAttr(messages []model.Message, finishReason string) (attribute.KeyValue, bool, error) {
	if len(messages) == 0 {
		return attribute.KeyValue{}, false, nil
	}
	out := make([]genAIOutputMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, genAIOutputMessage{
			Role:         msg.Role,
			Parts:        genAIParts(msg.Parts),
			FinishReason: finishReason,
		})
	}
	attr, err := genAIMessagesAttr(AttrGenAIOutputMessages, out)
	if err != nil {
		return attribute.KeyValue{}, false, err
	}
	return attr, true, nil
}

func genAIMessagesAttr(key attribute.Key, messages any) (attribute.KeyValue, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return attribute.KeyValue{}, err
	}
	return key.String(string(data)), nil
}

func genAIParts(parts []model.Part) []any {
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		out = append(out, genAIPart(part))
	}
	return out
}

func genAIPart(part model.Part) any {
	switch v := part.(type) {
	case model.TextPart:
		return genAITextPart{Type: "text", Content: v.Text}
	case model.ToolUsePart:
		return genAIToolCallPart{
			Type:      "tool_call",
			ID:        v.ID,
			Name:      v.Name,
			Arguments: v.Input,
		}
	case model.ToolResultPart:
		return genAIToolCallResponsePart{
			Type:     "tool_call_response",
			ID:       v.ToolUseID,
			Response: v.Content,
		}
	case model.ThinkingPart:
		return genAIThinkingPart(v)
	case model.ImagePart:
		return genAIBlobPart{
			Type:     "blob",
			Modality: "image",
			MIMEType: imageMIMEType(v.Format),
			Content:  base64.StdEncoding.EncodeToString(v.Bytes),
		}
	case model.DocumentPart:
		return genAIDocumentPart(v)
	case model.CitationsPart:
		return map[string]any{
			"type":      "citations",
			"content":   v.Text,
			"citations": v.Citations,
		}
	case model.CacheCheckpointPart:
		return map[string]any{
			"type": "cache_checkpoint",
		}
	default:
		return map[string]any{
			"type": "unknown",
		}
	}
}

func genAIThinkingPart(part model.ThinkingPart) any {
	if part.Text != "" {
		return genAIReasoningPart{Type: "reasoning", Content: part.Text}
	}
	out := map[string]any{
		"type":     "reasoning",
		"redacted": len(part.Redacted) > 0,
	}
	if part.Signature != "" {
		out["signature"] = part.Signature
	}
	if part.Index != 0 {
		out["index"] = part.Index
	}
	if part.Final {
		out["final"] = part.Final
	}
	return out
}

func genAIDocumentPart(part model.DocumentPart) any {
	mimeType := documentMIMEType(part.Format)
	switch {
	case part.URI != "":
		return genAIURIPart{
			Type:     "uri",
			Modality: "document",
			MIMEType: mimeType,
			URI:      part.URI,
		}
	case len(part.Bytes) > 0:
		return genAIBlobPart{
			Type:     "blob",
			Modality: "document",
			MIMEType: mimeType,
			Content:  base64.StdEncoding.EncodeToString(part.Bytes),
		}
	case part.Text != "":
		return genAIDocumentTextPart(part, part.Text, mimeType)
	case len(part.Chunks) > 0:
		out := genAIDocumentTextPart(part, strings.Join(part.Chunks, "\n\n"), mimeType)
		out["chunks"] = part.Chunks
		return out
	default:
		return map[string]any{
			"type":     "document",
			"modality": "document",
		}
	}
}

func genAIDocumentTextPart(part model.DocumentPart, content, mimeType string) map[string]any {
	out := map[string]any{
		"type":     "text",
		"modality": "document",
		"content":  content,
	}
	if part.Name != "" {
		out["name"] = part.Name
	}
	if mimeType != "" {
		out["mime_type"] = mimeType
	}
	if part.Context != "" {
		out["context"] = part.Context
	}
	if part.Cite {
		out["cite"] = part.Cite
	}
	return out
}

func imageMIMEType(format model.ImageFormat) string {
	switch format {
	case model.ImageFormatPNG:
		return "image/png"
	case model.ImageFormatJPEG:
		return "image/jpeg"
	case model.ImageFormatGIF:
		return "image/gif"
	case model.ImageFormatWEBP:
		return "image/webp"
	default:
		return ""
	}
}

func documentMIMEType(format model.DocumentFormat) string {
	switch format {
	case model.DocumentFormatPDF:
		return "application/pdf"
	case model.DocumentFormatCSV:
		return "text/csv"
	case model.DocumentFormatDOC:
		return "application/msword"
	case model.DocumentFormatDOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case model.DocumentFormatXLS:
		return "application/vnd.ms-excel"
	case model.DocumentFormatXLSX:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case model.DocumentFormatHTML:
		return "text/html"
	case model.DocumentFormatTXT:
		return "text/plain"
	case model.DocumentFormatMD:
		return "text/markdown"
	default:
		return ""
	}
}
