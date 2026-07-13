package vertex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// encodeContents translates the goa-ai transcript into Gemini contents.
// System messages fold into the returned system instruction; user and
// assistant messages become "user"/"model" contents. Tool use/result parts
// use the sanitized names from canonToProv so replayed history matches the
// advertised declarations. Tool call/response correlation uses the
// FunctionCall/FunctionResponse ID fields (both present in the installed
// genai SDK); FunctionResponse.Name still carries the sanitized tool name
// when it can be recovered from an earlier ToolUsePart in the same
// transcript, since Gemini documents Name as required and expected to match
// the originating FunctionDeclaration.
func encodeContents(msgs []*model.Message, canonToProv map[string]string) (*genai.Content, []*genai.Content, error) {
	var systemTexts []string
	contents := make([]*genai.Content, 0, len(msgs))
	toolUseNames := make(map[string]string) // tool-use ID -> provider tool name
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		if msg.Role == model.ConversationRoleSystem {
			for _, part := range msg.Parts {
				switch actual := part.(type) {
				case model.TextPart:
					systemTexts = append(systemTexts, actual.Text)
				case model.CitationsPart:
					return nil, nil, errors.New("vertex: replaying canonical citations is not supported")
				default:
					return nil, nil, fmt.Errorf("vertex: unsupported system message part %T", part)
				}
			}
			continue
		}
		var role string
		switch msg.Role {
		case model.ConversationRoleUser:
			role = "user"
		case model.ConversationRoleAssistant:
			role = "model"
		case model.ConversationRoleSystem:
			return nil, nil, errors.New("vertex: system message reached conversation encoding")
		default:
			return nil, nil, fmt.Errorf("vertex: unsupported message role %q", msg.Role)
		}
		parts := make([]*genai.Part, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			if tu, ok := part.(model.ToolUsePart); ok && tu.ID != "" {
				name, declared := canonToProv[tu.Name]
				if !declared {
					name = tu.Name
				}
				toolUseNames[tu.ID] = name
			}
			gp, err := encodePart(part, canonToProv, toolUseNames)
			if err != nil {
				return nil, nil, err
			}
			if gp != nil {
				parts = append(parts, gp)
			}
		}
		if len(parts) == 0 {
			continue
		}
		contents = append(contents, &genai.Content{Role: role, Parts: parts})
	}
	var system *genai.Content
	if len(systemTexts) > 0 {
		system = &genai.Content{Parts: []*genai.Part{{Text: strings.Join(systemTexts, "\n\n")}}}
	}
	return system, contents, nil
}

// encodePart translates one goa-ai message part into a Gemini part.
// toolUseNames maps tool-use IDs seen earlier in the transcript to their
// provider tool name, used to recover FunctionResponse.Name for the
// matching ToolResultPart.
func encodePart(part model.Part, canonToProv map[string]string, toolUseNames map[string]string) (*genai.Part, error) {
	switch p := part.(type) {
	case model.TextPart:
		return &genai.Part{Text: p.Text}, nil
	case model.CitationsPart:
		return nil, errors.New("vertex: replaying canonical citations is not supported")
	case model.ImagePart:
		return &genai.Part{InlineData: &genai.Blob{
			MIMEType: "image/" + string(p.Format),
			Data:     p.Bytes,
		}}, nil
	case model.DocumentPart:
		return encodeDocumentPart(p)
	case model.ToolUsePart:
		args, err := toArgsMap(p.Input)
		if err != nil {
			return nil, fmt.Errorf("vertex: encode tool use %q: %w", p.Name, err)
		}
		providerName, ok := canonToProv[p.Name]
		if !ok {
			for canonical, provider := range canonToProv {
				if provider == p.Name {
					return nil, fmt.Errorf(
						"vertex: historical provider tool name %q collides with current tool %q",
						p.Name,
						canonical,
					)
				}
			}
			providerName = p.Name
		}
		gp := &genai.Part{FunctionCall: &genai.FunctionCall{
			ID:   p.ID,
			Name: providerName,
			Args: args,
		}}
		// Signature contract: mirrors the ThinkingPart case below. This
		// adapter's response translator is the only producer of
		// ToolUsePart.ThoughtSignature and always encodes it with
		// base64.StdEncoding, so invalid base64 here is a broken invariant in
		// this adapter's own translation, not a shape that can legitimately
		// arrive from elsewhere; fail fast instead of dropping the signature.
		if p.ThoughtSignature != "" {
			sig, err := base64.StdEncoding.DecodeString(p.ThoughtSignature)
			if err != nil {
				return nil, fmt.Errorf("vertex: encode tool use %q: thought signature is not valid base64: %w", p.Name, err)
			}
			gp.ThoughtSignature = sig
		}
		return gp, nil
	case model.ToolResultPart:
		resp, err := toResponseMap(p.Content, p.IsError)
		if err != nil {
			return nil, fmt.Errorf("vertex: encode tool result %q: %w", p.ToolUseID, err)
		}
		// The canonical transcript pairs every tool result with a prior tool use
		// in the same request. An orphan result is a runtime bug, not a state this
		// adapter can legally observe. Fail fast instead of synthesizing a name
		// from the tool-use ID.
		providerName, ok := toolUseNames[p.ToolUseID]
		if !ok {
			return nil, fmt.Errorf("vertex: tool result %q has no matching tool use in transcript", p.ToolUseID)
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID:       p.ToolUseID,
			Name:     providerName,
			Response: resp,
		}}, nil
	case model.ThinkingPart:
		if len(p.Redacted) > 0 {
			return nil, errors.New("vertex: redacted thinking is not supported")
		}
		if p.Text == "" || p.Signature == "" {
			return nil, errors.New("vertex: thinking replay requires plaintext and signature")
		}
		// Signature contract: model.ThinkingPart.Signature is an opaque
		// string, while genai.Part.ThoughtSignature is []byte. This adapter
		// defines the string form as standard base64 of the raw signature
		// bytes; the response translator (this same adapter) is the only
		// producer of ThinkingPart.Signature and always encodes with
		// base64.StdEncoding. Invalid base64 is therefore a broken
		// invariant in this adapter's own translation, not a shape that can
		// legitimately arrive from elsewhere; fail fast instead of falling
		// back to raw string bytes.
		gp := &genai.Part{Thought: true, Text: p.Text}
		if p.Signature != "" {
			sig, err := base64.StdEncoding.DecodeString(p.Signature)
			if err != nil {
				return nil, fmt.Errorf("vertex: encode thinking part: signature is not valid base64: %w", err)
			}
			gp.ThoughtSignature = sig
		}
		return gp, nil
	case model.CacheCheckpointPart:
		return nil, errors.New("vertex: cache checkpoints are not supported")
	default:
		return nil, fmt.Errorf("vertex: unsupported message part %T", part)
	}
}

// toArgsMap decodes a tool input value into the map form Gemini requires.
// Tool inputs are schema'd JSON objects by construction (Goa tool specs
// always describe an object payload), so a non-object value here is a
// broken invariant, not a shape to coerce around.
func toArgsMap(raw rawjson.Message) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var m map[string]any
	if err := decoder.Decode(&m); err != nil {
		return nil, fmt.Errorf("tool input must be a JSON object: %w", err)
	}
	if m == nil {
		return nil, errors.New("tool input must be a JSON object")
	}
	return m, nil
}

// encodeDocumentPart maps every canonical document source supported by Gemini
// and rejects canonical metadata that Gemini cannot represent.
func encodeDocumentPart(part model.DocumentPart) (*genai.Part, error) {
	if part.Cite {
		return nil, fmt.Errorf("vertex: document %q citation configuration is not supported", part.Name)
	}
	if part.Context != "" {
		return nil, fmt.Errorf("vertex: document %q context is not supported", part.Name)
	}
	switch {
	case len(part.Bytes) > 0:
		mime, err := documentMIMEType(part.Format)
		if err != nil {
			return nil, fmt.Errorf("vertex: document %q: %w", part.Name, err)
		}
		return &genai.Part{InlineData: &genai.Blob{MIMEType: mime, Data: part.Bytes}}, nil
	case part.Text != "":
		return &genai.Part{Text: part.Text}, nil
	case len(part.Chunks) > 0:
		return &genai.Part{Text: strings.Join(part.Chunks, "\n\n")}, nil
	case part.URI != "":
		if !strings.HasPrefix(part.URI, "gs://") {
			return nil, fmt.Errorf("vertex: document %q URI must use gs://", part.Name)
		}
		mime, err := documentMIMEType(part.Format)
		if err != nil {
			return nil, fmt.Errorf("vertex: document %q: %w", part.Name, err)
		}
		return &genai.Part{FileData: &genai.FileData{
			DisplayName: part.Name,
			FileURI:     part.URI,
			MIMEType:    mime,
		}}, nil
	default:
		return nil, fmt.Errorf("vertex: document %q has no source", part.Name)
	}
}

// documentMIMEType maps canonical document formats to IANA media types.
func documentMIMEType(format model.DocumentFormat) (string, error) {
	switch format {
	case model.DocumentFormatPDF:
		return "application/pdf", nil
	case model.DocumentFormatCSV:
		return "text/csv", nil
	case model.DocumentFormatDOC:
		return "application/msword", nil
	case model.DocumentFormatDOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	case model.DocumentFormatXLS:
		return "application/vnd.ms-excel", nil
	case model.DocumentFormatXLSX:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	case model.DocumentFormatHTML:
		return "text/html", nil
	case model.DocumentFormatTXT:
		return "text/plain", nil
	case model.DocumentFormatMD:
		return "text/markdown", nil
	default:
		return "", fmt.Errorf("unsupported document format %q", format)
	}
}

// toResponseMap coerces a tool result into a JSON object, wrapping
// non-object values under "output" and flagging errors under "error". This
// is a legitimate boundary translation, not a fallback: Gemini requires
// FunctionResponse.Response to be an object, and tool results (unlike tool
// inputs) are not schema'd — arbitrary JSON values are a normal result
// shape, so wrapping is the correct on-wire representation, not a
// work-around for a broken invariant. The returned map is always freshly
// allocated so the caller's Content value is never mutated.
func toResponseMap(v any, isError bool) (map[string]any, error) {
	var m map[string]any
	if mm, ok := v.(map[string]any); ok {
		m = make(map[string]any, len(mm)+1)
		for k, val := range mm {
			m[k] = val
		}
	} else {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		if object, ok := decoded.(map[string]any); ok {
			m = object
		} else {
			m = map[string]any{"output": decoded}
		}
	}
	if isError {
		m["error"] = true
	}
	return m, nil
}
