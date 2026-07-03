package vertex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// translateResponse converts a Gemini response into the provider-neutral
// model.Response. Only the first candidate is used (CandidateCount is never
// set above 1 by this adapter).
func translateResponse(resp *genai.GenerateContentResponse, modelID string, class model.ModelClass, provToCanon map[string]string) (*model.Response, error) {
	if resp == nil || len(resp.Candidates) == 0 {
		return nil, errors.New("vertex: response has no candidates")
	}
	cand := resp.Candidates[0]
	out := &model.Response{StopReason: string(cand.FinishReason)}
	if cand.Content != nil {
		msg := model.Message{Role: model.ConversationRoleAssistant}
		callIndex := 0
		for _, part := range cand.Content.Parts {
			// Other genai.Part variants (inline/file data, code execution
			// results) are intentionally dropped: no model.Part equivalent.
			switch {
			case part.FunctionCall != nil:
				payload, err := marshalArgs(part.FunctionCall.Args)
				if err != nil {
					return nil, err
				}
				callIndex++
				out.ToolCalls = append(out.ToolCalls, model.ToolCall{
					Name:    toolIdent(part.FunctionCall.Name, provToCanon),
					Payload: payload,
					ID:      toolCallID(part.FunctionCall, callIndex),
				})
			case part.Thought:
				msg.Parts = append(msg.Parts, model.ThinkingPart{
					Text:      part.Text,
					Signature: base64.StdEncoding.EncodeToString(part.ThoughtSignature),
					Final:     true,
				})
			case part.Text != "":
				msg.Parts = append(msg.Parts, model.TextPart{Text: part.Text})
			}
		}
		if len(msg.Parts) > 0 {
			out.Content = append(out.Content, msg)
		}
	}
	out.Usage = translateUsage(resp.UsageMetadata, modelID, class)
	return out, nil
}

// canonicalToolName reverses sanitization; names never advertised this
// request pass through unchanged so the runtime can flag the unknown tool.
func canonicalToolName(prov string, provToCanon map[string]string) string {
	if canon, ok := provToCanon[prov]; ok {
		return canon
	}
	return prov
}

// toolIdent maps a provider tool name back to its canonical ident.
func toolIdent(prov string, provToCanon map[string]string) tools.Ident {
	return tools.Ident(canonicalToolName(prov, provToCanon))
}

// marshalArgs encodes Gemini function-call args as a JSON payload.
func marshalArgs(args map[string]any) (rawjson.Message, error) {
	if len(args) == 0 {
		return rawjson.Message(`{}`), nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("vertex: marshal tool args: %w", err)
	}
	return b, nil
}

// toolCallID returns a stable per-response identifier for a function call,
// preferring the provider-issued ID when present since Gemini does not
// always populate it.
func toolCallID(fc *genai.FunctionCall, index int) string {
	if fc.ID != "" {
		return fc.ID
	}
	return fmt.Sprintf("call-%d-%s", index, fc.Name)
}

// translateUsage maps Gemini usage metadata onto model.TokenUsage, counting
// thought tokens as output and stamping model attribution.
func translateUsage(md *genai.GenerateContentResponseUsageMetadata, modelID string, class model.ModelClass) model.TokenUsage {
	usage := model.TokenUsage{Model: modelID, ModelClass: class}
	if md == nil {
		return usage
	}
	usage.InputTokens = int(md.PromptTokenCount)
	usage.OutputTokens = int(md.CandidatesTokenCount) + int(md.ThoughtsTokenCount)
	usage.TotalTokens = int(md.TotalTokenCount)
	usage.CacheReadTokens = int(md.CachedContentTokenCount)
	return usage
}
