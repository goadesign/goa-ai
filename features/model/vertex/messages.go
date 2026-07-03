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
		gp := &genai.Part{FunctionCall: &genai.FunctionCall{
			ID:   p.ID,
			Name: providerToolName(p.Name, canonToProv),
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
		// The transcript ledger pairs every tool result with a prior tool use
		// in the same request; an orphan result (no matching ToolUsePart
		// earlier in the transcript) is a runtime bug, not a state this
		// adapter can legally observe. Fail fast instead of synthesizing a
		// name from the tool-use ID.
		canonName, ok := toolUseNames[p.ToolUseID]
		if !ok {
			return nil, fmt.Errorf("vertex: tool result %q has no matching tool use in transcript", p.ToolUseID)
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID:       p.ToolUseID,
			Name:     providerToolName(canonName, canonToProv),
			Response: resp,
		}}, nil
	case model.ThinkingPart:
		if p.Text == "" && p.Signature == "" {
			// Redacted-only (or otherwise empty) thinking part: Gemini has
			// no redacted-thinking round-trip (no Part field accepts opaque
			// redacted bytes), so there is nothing valid to replay. Emitting
			// an empty Part{Thought: true} would just be wire noise, so
			// drop the part entirely instead.
			return nil, nil
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
		return nil, nil // Gemini uses implicit caching; no inline markers.
	default:
		return nil, nil // Unsupported parts (documents, citations) are skipped.
	}
}

// providerToolName returns the sanitized name for a canonical tool name.
// This is a legitimate boundary translation, not a fallback: canonical
// names outside this request's tool definitions are legal (replayed
// history whose tool list changed between turns), so they are sanitized
// on the fly the same way current-request names are, instead of being
// treated as an error.
func providerToolName(name string, canonToProv map[string]string) string {
	if prov, ok := canonToProv[name]; ok {
		return prov
	}
	return sanitizeToolName(name)
}

// toArgsMap decodes a tool input value into the map form Gemini requires.
// Tool inputs are schema'd JSON objects by construction (Goa tool specs
// always describe an object payload), so a non-object value here is a
// broken invariant, not a shape to coerce around.
func toArgsMap(v any) (map[string]any, error) {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("tool input must be a JSON object: %w", err)
	}
	if m == nil {
		// JSON null unmarshals into a nil map without error. No-arg tool
		// calls are legal, and Gemini requires Args to be an object, so
		// normalize null to an empty object instead of sending nil.
		m = map[string]any{}
	}
	return m, nil
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
		if err := json.Unmarshal(raw, &m); err != nil {
			m = map[string]any{"output": v}
		}
	}
	if m == nil {
		// JSON null unmarshals into a nil map without error (nil Content or
		// content that marshals as null). Gemini requires Response to be an
		// object, and m["error"] below would panic on a nil map, so start
		// from an empty object and keep the non-nil value under "output".
		m = map[string]any{}
		if v != nil {
			m["output"] = v
		}
	}
	if isError {
		m["error"] = true
	}
	return m, nil
}
