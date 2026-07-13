package vertex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"

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
	if len(resp.Candidates) != 1 {
		return nil, fmt.Errorf("vertex: response has %d candidates, want exactly one", len(resp.Candidates))
	}
	cand := resp.Candidates[0]
	out := &model.Response{StopReason: string(cand.FinishReason)}
	if out.StopReason == "" {
		return nil, errors.New("vertex: response candidate is missing its finish reason")
	}
	if cand.Content != nil {
		msg := model.Message{Role: model.ConversationRoleAssistant}
		for _, part := range cand.Content.Parts {
			if part == nil {
				return nil, errors.New("vertex: response contains a nil part")
			}
			switch {
			case part.FunctionCall != nil:
				if part.FunctionCall.Name == "" {
					return nil, errors.New("vertex: response function call is missing its name")
				}
				if part.FunctionCall.ID == "" {
					return nil, fmt.Errorf("vertex: response function call %q is missing its ID", part.FunctionCall.Name)
				}
				payload, err := marshalArgs(part.FunctionCall.Args)
				if err != nil {
					return nil, err
				}
				msg.Parts = append(msg.Parts, model.ToolUsePart{
					Name:             string(toolIdent(part.FunctionCall.Name, provToCanon)),
					Input:            payload,
					ID:               part.FunctionCall.ID,
					ThoughtSignature: encodeThoughtSignature(part.ThoughtSignature),
				})
			case part.Thought:
				if part.Text == "" || len(part.ThoughtSignature) == 0 {
					return nil, errors.New("vertex: response thinking requires plaintext and signature")
				}
				msg.Parts = append(msg.Parts, model.ThinkingPart{
					Text:      part.Text,
					Signature: base64.StdEncoding.EncodeToString(part.ThoughtSignature),
					Final:     true,
				})
			case part.Text != "":
				msg.Parts = append(msg.Parts, model.TextPart{Text: part.Text})
			default:
				return nil, errors.New("vertex: unsupported response part")
			}
		}
		grounded, err := applyGroundingMetadata(msg.Parts, cand.GroundingMetadata)
		if err != nil {
			return nil, err
		}
		msg.Parts = grounded
		if len(msg.Parts) > 0 {
			out.Content = append(out.Content, msg)
		}
	}
	out.Usage = translateUsage(resp.UsageMetadata, modelID, class)
	if err := model.ValidateResponse(out); err != nil {
		return nil, fmt.Errorf("vertex: invalid response: %w", err)
	}
	return out, nil
}

// applyGroundingMetadata converts Gemini source attribution into canonical
// citation parts and rejects provider metadata the canonical model cannot
// represent.
func applyGroundingMetadata(parts []model.Part, metadata *genai.GroundingMetadata) ([]model.Part, error) {
	if metadata == nil {
		return parts, nil
	}
	if len(metadata.ImageSearchQueries) > 0 ||
		metadata.RetrievalMetadata != nil ||
		metadata.SearchEntryPoint != nil ||
		len(metadata.WebSearchQueries) > 0 ||
		metadata.GoogleMapsWidgetContextToken != "" ||
		len(metadata.RetrievalQueries) > 0 ||
		len(metadata.SourceFlaggingUris) > 0 {
		return nil, errors.New("vertex: response grounding contains unsupported auxiliary metadata")
	}
	firstText := -1
	for i, part := range parts {
		if _, ok := part.(model.TextPart); ok {
			firstText = i
			break
		}
	}
	if firstText == -1 {
		return nil, errors.New("vertex: response grounding has no text part")
	}
	citationsByPart := make(map[int][]model.Citation)
	referenced := make(map[int]struct{})
	for _, support := range metadata.GroundingSupports {
		if support == nil {
			return nil, errors.New("vertex: response grounding contains a nil support")
		}
		if len(support.ConfidenceScores) > 0 || len(support.RenderedParts) > 0 {
			return nil, errors.New("vertex: response grounding support contains unsupported metadata")
		}
		partIndex := firstText
		if support.Segment != nil {
			partIndex = int(support.Segment.PartIndex)
			if partIndex < 0 || partIndex >= len(parts) {
				return nil, fmt.Errorf("vertex: grounding support references part %d", partIndex)
			}
			if _, ok := parts[partIndex].(model.TextPart); !ok {
				return nil, fmt.Errorf("vertex: grounding support references non-text part %d", partIndex)
			}
		}
		for _, chunkIndex := range support.GroundingChunkIndices {
			index := int(chunkIndex)
			citation, err := groundingCitation(metadata.GroundingChunks, index)
			if err != nil {
				return nil, err
			}
			citationsByPart[partIndex] = append(citationsByPart[partIndex], citation)
			referenced[index] = struct{}{}
		}
	}
	for index := range metadata.GroundingChunks {
		if _, ok := referenced[index]; ok {
			continue
		}
		return nil, fmt.Errorf("vertex: response grounding contains unreferenced chunk %d", index)
	}
	out := append([]model.Part(nil), parts...)
	for index, citations := range citationsByPart {
		text := out[index].(model.TextPart)
		out[index] = model.CitationsPart{Text: text.Text, Citations: citations}
	}
	return out, nil
}

// groundingCitation maps one Gemini evidence source into a canonical citation.
func groundingCitation(chunks []*genai.GroundingChunk, index int) (model.Citation, error) {
	if index < 0 || index >= len(chunks) {
		return model.Citation{}, fmt.Errorf("vertex: grounding support references chunk %d", index)
	}
	chunk := chunks[index]
	if chunk == nil {
		return model.Citation{}, fmt.Errorf("vertex: grounding chunk %d is nil", index)
	}
	switch {
	case chunk.Web != nil:
		return model.Citation{
			Title:  chunk.Web.Title,
			Source: chunk.Web.URI,
		}, nil
	case chunk.RetrievedContext != nil:
		source := chunk.RetrievedContext.URI
		if source == "" {
			source = chunk.RetrievedContext.DocumentName
		}
		citation := model.Citation{
			Title:  chunk.RetrievedContext.Title,
			Source: source,
		}
		if chunk.RetrievedContext.Text != "" {
			citation.SourceContent = []string{chunk.RetrievedContext.Text}
		}
		return citation, nil
	default:
		return model.Citation{}, fmt.Errorf("vertex: unsupported grounding chunk %d", index)
	}
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
	if err := validateSDKJSONNumbers(args); err != nil {
		return nil, err
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("vertex: marshal tool args: %w", err)
	}
	return b, nil
}

// validateSDKJSONNumbers rejects integers that Gemini's float64-backed SDK
// representation cannot prove were decoded without precision loss.
func validateSDKJSONNumbers(value any) error {
	const maxExactInteger = 1<<53 - 1

	switch actual := value.(type) {
	case float64:
		if math.Trunc(actual) == actual && math.Abs(actual) > maxExactInteger {
			return fmt.Errorf("vertex: tool args contain an integer outside the exact SDK range: %v", actual)
		}
	case map[string]any:
		for _, item := range actual {
			if err := validateSDKJSONNumbers(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range actual {
			if err := validateSDKJSONNumbers(item); err != nil {
				return err
			}
		}
	}
	return nil
}

// encodeThoughtSignature converts a genai Part's raw ThoughtSignature bytes
// into the opaque base64 string carried on model.ToolCall/model.ToolUsePart.
// Gemini 3-class models attach a signature to the same Part that carries a
// FunctionCall; gemini-2.5-class targets never populate it, so an empty
// input (the common case) yields an empty string, matching the "empty means
// absent" contract.
func encodeThoughtSignature(sig []byte) string {
	if len(sig) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(sig)
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
