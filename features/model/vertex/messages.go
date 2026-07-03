package vertex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
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
	toolUseNames := make(map[string]string) // tool-use ID -> canonical tool name
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		if msg.Role == model.ConversationRoleSystem {
			// Only text parts contribute to the system instruction;
			// non-text parts in system messages are dropped.
			for _, part := range msg.Parts {
				if tp, ok := part.(model.TextPart); ok {
					systemTexts = append(systemTexts, tp.Text)
				}
			}
			continue
		}
		role := "user"
		if msg.Role == model.ConversationRoleAssistant {
			role = "model"
		}
		parts := make([]*genai.Part, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			if tu, ok := part.(model.ToolUsePart); ok && tu.ID != "" {
				toolUseNames[tu.ID] = tu.Name
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
// canonical tool name, used to recover FunctionResponse.Name for the
// matching ToolResultPart.
func encodePart(part model.Part, canonToProv map[string]string, toolUseNames map[string]string) (*genai.Part, error) {
	switch p := part.(type) {
	case model.TextPart:
		return &genai.Part{Text: p.Text}, nil
	case model.ImagePart:
		return &genai.Part{InlineData: &genai.Blob{
			MIMEType: "image/" + string(p.Format),
			Data:     p.Bytes,
		}}, nil
	case model.ToolUsePart:
		args, err := toArgsMap(p.Input)
		if err != nil {
			return nil, fmt.Errorf("vertex: encode tool use %q: %w", p.Name, err)
		}
		return &genai.Part{FunctionCall: &genai.FunctionCall{
			ID:   p.ID,
			Name: providerToolName(p.Name, canonToProv),
			Args: args,
		}}, nil
	case model.ToolResultPart:
		resp, err := toResponseMap(p.Content, p.IsError)
		if err != nil {
			return nil, fmt.Errorf("vertex: encode tool result %q: %w", p.ToolUseID, err)
		}
		name := p.ToolUseID
		if canonName, ok := toolUseNames[p.ToolUseID]; ok {
			name = providerToolName(canonName, canonToProv)
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID:       p.ToolUseID,
			Name:     name,
			Response: resp,
		}}, nil
	case model.ThinkingPart:
		// Signature contract: model.ThinkingPart.Signature is an opaque
		// string, while genai.Part.ThoughtSignature is []byte. This adapter
		// defines the string form as standard base64 of the raw signature
		// bytes (the response translator encodes with base64.StdEncoding
		// correspondingly). If the string is not valid base64 (e.g. history
		// produced by another adapter), fall back to the raw string bytes
		// rather than dropping the signature.
		gp := &genai.Part{Thought: true, Text: p.Text}
		if p.Signature != "" {
			sig, err := base64.StdEncoding.DecodeString(p.Signature)
			if err != nil {
				sig = []byte(p.Signature)
			}
			gp.ThoughtSignature = sig
		}
		return gp, nil
	case model.CacheCheckpointPart:
		return nil, nil // Gemini uses implicit caching; no inline markers.
	default:
		return nil, nil // Unsupported parts (documents, citations) are skipped.
	}
}

// providerToolName returns the sanitized name for a canonical tool name,
// falling back to on-the-fly sanitization for names outside this request's
// definitions (replayed history from other requests).
func providerToolName(name string, canonToProv map[string]string) string {
	if prov, ok := canonToProv[name]; ok {
		return prov
	}
	return sanitizeToolName(name)
}

// toArgsMap coerces a tool input value into the map form Gemini requires.
func toArgsMap(v any) (map[string]any, error) {
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// Adapter extension: Gemini args must be a JSON object, so
		// non-object inputs are wrapped under "input".
		return map[string]any{"input": v}, nil //nolint:nilerr // non-object inputs are wrapped
	}
	return m, nil
}

// toResponseMap coerces a tool result into a JSON object, wrapping
// non-object values under "output" and flagging errors under "error".
// The returned map is always freshly allocated so the caller's Content
// value is never mutated.
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
		if err := json.Unmarshal(raw, &m); err != nil {
			m = map[string]any{"output": v}
		}
	}
	if isError {
		m["error"] = true
	}
	return m, nil
}
