package vertex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"unicode/utf8"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	vertexToolCallIDAllocator struct {
		used  map[string]struct{}
		index int
	}

	// fixedJSONWriter copies encoded arguments into the exact-size allocation
	// established before encoding begins.
	fixedJSONWriter struct {
		data   []byte
		offset int
	}
)

const maxVertexToolArgsBytes = 16 << 20

var errVertexToolArgsTooLarge = errors.New("vertex: tool args exceed 16777216 bytes")

// newVertexToolCallIDAllocator reserves every tool-call ID already present in
// request history so locally assigned Gemini IDs remain unique in the run.
func newVertexToolCallIDAllocator(messages []*model.Message) *vertexToolCallIDAllocator {
	allocator := &vertexToolCallIDAllocator{used: make(map[string]struct{})}
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, part := range message.Parts {
			if toolUse, ok := part.(model.ToolUsePart); ok && toolUse.ID != "" {
				allocator.used[toolUse.ID] = struct{}{}
			}
		}
	}
	return allocator
}

// reserve records one provider-issued ID and rejects duplicates with request
// history or earlier calls in the same response.
func (a *vertexToolCallIDAllocator) reserve(id string) error {
	if _, exists := a.used[id]; exists {
		return fmt.Errorf("vertex: response function call ID %q is not unique", id)
	}
	a.used[id] = struct{}{}
	return nil
}

// next returns the next deterministic local ID that does not collide with
// request history or provider-issued IDs.
func (a *vertexToolCallIDAllocator) next() string {
	for {
		id := fmt.Sprintf("vertex-call-%d", a.index)
		a.index++
		if _, exists := a.used[id]; exists {
			continue
		}
		a.used[id] = struct{}{}
		return id
	}
}

// translateResponse converts a Gemini response into the provider-neutral
// model.Response. Only the first candidate is used (CandidateCount is never
// set above 1 by this adapter).
func translateResponse(resp *genai.GenerateContentResponse, modelID string, class model.ModelClass, provToCanon map[string]string, callIDs *vertexToolCallIDAllocator) (*model.Response, error) {
	if resp == nil || len(resp.Candidates) == 0 {
		return nil, errors.New("vertex: response has no candidates")
	}
	if len(resp.Candidates) != 1 {
		return nil, fmt.Errorf("vertex: response has %d candidates, want exactly one", len(resp.Candidates))
	}
	cand := resp.Candidates[0]
	out := &model.Response{
		StopReason:    string(cand.FinishReason),
		OutputLimited: vertexOutputLimited(string(cand.FinishReason)),
	}
	if out.StopReason == "" {
		return nil, errors.New("vertex: response candidate is missing its finish reason")
	}
	if cand.Content != nil {
		msg := model.Message{Role: model.ConversationRoleAssistant}
		thinkingIndex := 0
		if callIDs == nil {
			callIDs = newVertexToolCallIDAllocator(nil)
		}
		for _, part := range cand.Content.Parts {
			if part == nil || part.FunctionCall == nil || part.FunctionCall.ID == "" {
				continue
			}
			if err := callIDs.reserve(part.FunctionCall.ID); err != nil {
				return nil, err
			}
		}
		for _, part := range cand.Content.Parts {
			if part == nil {
				return nil, errors.New("vertex: response contains a nil part")
			}
			switch {
			case part.FunctionCall != nil:
				if part.FunctionCall.Name == "" {
					return nil, errors.New("vertex: response function call is missing its name")
				}
				name, ok := toolIdent(part.FunctionCall.Name, provToCanon)
				if !ok {
					return nil, fmt.Errorf(
						"vertex: translate response function call: %w",
						model.NewUnadvertisedToolNameError(part.FunctionCall.Name),
					)
				}
				payload, err := marshalArgs(part.FunctionCall.Args)
				if err != nil {
					return nil, err
				}
				callID := part.FunctionCall.ID
				if callID == "" {
					callID = callIDs.next()
				}
				msg.Parts = append(msg.Parts, model.ToolUsePart{
					Name:             string(name),
					Input:            payload,
					ID:               callID,
					ThoughtSignature: encodeThoughtSignature(part.ThoughtSignature),
				})
			case part.Thought:
				if part.Text == "" || len(part.ThoughtSignature) == 0 {
					return nil, errors.New("vertex: response thinking requires plaintext and signature")
				}
				msg.Parts = append(msg.Parts, model.ThinkingPart{
					Text:      part.Text,
					Signature: base64.StdEncoding.EncodeToString(part.ThoughtSignature),
					Index:     thinkingIndex,
					Final:     true,
				})
				thinkingIndex++
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
	return out, nil
}

// vertexOutputLimited identifies the Gemini finish reason emitted when a
// response consumes its configured generated-token budget.
func vertexOutputLimited(reason string) bool {
	return reason == string(genai.FinishReasonMaxTokens)
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

// canonicalToolName reverses sanitization only for a tool advertised by this
// request.
func canonicalToolName(prov string, provToCanon map[string]string) (string, bool) {
	canon, ok := provToCanon[prov]
	return canon, ok
}

// toolIdent maps a provider tool name back to its canonical ident.
func toolIdent(prov string, provToCanon map[string]string) (tools.Ident, bool) {
	canonical, ok := canonicalToolName(prov, provToCanon)
	return tools.Ident(canonical), ok
}

// marshalArgs encodes Gemini function-call args as a JSON payload.
func marshalArgs(args map[string]any) (rawjson.Message, error) {
	if len(args) == 0 {
		return rawjson.Message(`{}`), nil
	}
	values := 0
	encodedSize := 0
	if err := measureSDKJSONValue(args, 0, &values, &encodedSize); err != nil {
		return nil, err
	}
	data := make([]byte, encodedSize+1)
	writer := &fixedJSONWriter{data: data}
	if err := json.NewEncoder(writer).Encode(args); err != nil {
		return nil, fmt.Errorf("vertex: marshal tool args: %w", err)
	}
	if writer.offset != len(data) || data[encodedSize] != '\n' {
		return nil, errors.New("vertex: tool args encoding size changed between passes")
	}
	return rawjson.Message(data[:encodedSize]), nil
}

// measureSDKJSONValue computes the exact encoding/json byte count without
// building the encoded payload. It also bounds nesting and value count before
// marshalArgs allocates its result buffer.
func measureSDKJSONValue(value any, depth int, values, encodedSize *int) error {
	const maxExactInteger = 1<<53 - 1
	if depth > 64 {
		return errors.New("vertex: tool args exceed 64 nested levels")
	}
	if *values >= 100_000 {
		return errors.New("vertex: tool args exceed 100000 values")
	}
	*values++

	switch actual := value.(type) {
	case float64:
		if math.IsNaN(actual) || math.IsInf(actual, 0) {
			return fmt.Errorf("vertex: tool args contain unsupported number %v", actual)
		}
		if math.Trunc(actual) == actual && math.Abs(actual) > maxExactInteger {
			return fmt.Errorf("vertex: tool args contain an integer outside the exact SDK range: %v", actual)
		}
		return addVertexJSONBytes(encodedSize, encodedFloat64Size(actual))
	case string:
		return measureJSONString(actual, encodedSize)
	case bool:
		if actual {
			return addVertexJSONBytes(encodedSize, len("true"))
		}
		return addVertexJSONBytes(encodedSize, len("false"))
	case nil:
		return addVertexJSONBytes(encodedSize, len("null"))
	case map[string]any:
		if len(actual) > 100_000-*values {
			return errors.New("vertex: tool args exceed 100000 values")
		}
		if err := addVertexJSONBytes(encodedSize, 2); err != nil {
			return err
		}
		index := 0
		for key, item := range actual {
			if index > 0 {
				if err := addVertexJSONBytes(encodedSize, 1); err != nil {
					return err
				}
			}
			if err := measureJSONString(key, encodedSize); err != nil {
				return err
			}
			if err := addVertexJSONBytes(encodedSize, 1); err != nil {
				return err
			}
			if err := measureSDKJSONValue(item, depth+1, values, encodedSize); err != nil {
				return err
			}
			index++
		}
	case []any:
		if len(actual) > 100_000-*values {
			return errors.New("vertex: tool args exceed 100000 values")
		}
		if err := addVertexJSONBytes(encodedSize, 2); err != nil {
			return err
		}
		for index, item := range actual {
			if index > 0 {
				if err := addVertexJSONBytes(encodedSize, 1); err != nil {
					return err
				}
			}
			if err := measureSDKJSONValue(item, depth+1, values, encodedSize); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("vertex: tool args contain unsupported value %T", value)
	}
	return nil
}

// measureJSONString counts the bytes encoding/json emits with HTML escaping
// enabled, including the surrounding quotes.
func measureJSONString(value string, encodedSize *int) error {
	if err := addVertexJSONBytes(encodedSize, 2); err != nil {
		return err
	}
	for index := 0; index < len(value); {
		char := value[index]
		if char < utf8.RuneSelf {
			index++
			switch char {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				if err := addVertexJSONBytes(encodedSize, 2); err != nil {
					return err
				}
			case '<', '>', '&':
				if err := addVertexJSONBytes(encodedSize, 6); err != nil {
					return err
				}
			default:
				size := 1
				if char < 0x20 {
					size = 6
				}
				if err := addVertexJSONBytes(encodedSize, size); err != nil {
					return err
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		index += size
		if r == utf8.RuneError && size == 1 {
			if err := addVertexJSONBytes(encodedSize, 6); err != nil {
				return err
			}
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			if err := addVertexJSONBytes(encodedSize, 6); err != nil {
				return err
			}
			continue
		}
		if err := addVertexJSONBytes(encodedSize, size); err != nil {
			return err
		}
	}
	return nil
}

func encodedFloat64Size(value float64) int {
	format := byte('f')
	absolute := math.Abs(value)
	if absolute != 0 && (absolute < 1e-6 || absolute >= 1e21) {
		format = 'e'
	}
	var buffer [32]byte
	encoded := strconv.AppendFloat(buffer[:0], value, format, -1, 64)
	if format == 'e' && len(encoded) >= 4 {
		end := len(encoded)
		if encoded[end-4] == 'e' && encoded[end-3] == '-' && encoded[end-2] == '0' {
			return end - 1
		}
	}
	return len(encoded)
}

func addVertexJSONBytes(encodedSize *int, count int) error {
	if count > maxVertexToolArgsBytes-*encodedSize {
		return errVertexToolArgsTooLarge
	}
	*encodedSize += count
	return nil
}

func (w *fixedJSONWriter) Write(data []byte) (int, error) {
	if len(data) > len(w.data)-w.offset {
		return 0, io.ErrShortBuffer
	}
	copy(w.data[w.offset:], data)
	w.offset += len(data)
	return len(data), nil
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
