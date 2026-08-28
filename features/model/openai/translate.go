// Package openai translates OpenAI Responses API objects back into the
// provider-neutral model.Response and model.ToolCall structures expected by
// planners and runtimes.
package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/responses"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

const openAIMaxOutputTokensReason = "max_output_tokens"

func translateResponse(
	resp *responses.Response,
	codec *toolCodec,
	resolvedModelID string,
	resolvedModelClass model.ModelClass,
	output *model.StructuredOutput,
	outputProjection *strictSchemaProjection,
) (*model.Response, error) {
	if resp == nil {
		return nil, errors.New("openai: response is nil")
	}
	if resp.Status == responses.ResponseStatusFailed || resp.Error.Message != "" {
		return nil, providerErrorFromResponseFailure(
			"responses.create",
			string(resp.Error.Code),
			resp.Error.Message,
			errors.New(resp.Error.Message),
		)
	}
	if err := preflightResponseSnapshot(resp); err != nil {
		return nil, err
	}
	translated := &model.Response{
		Usage: translateUsage(resp.Usage, chooseModelID(resp.Model, resolvedModelID), resolvedModelClass),
	}
	var (
		pendingThinking []model.Part
		reasoningRaw    []string
		thinkingIndex   int
	)
	flushThinking := func() {
		if len(pendingThinking) == 0 {
			return
		}
		message := model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: append([]model.Part(nil), pendingThinking...),
		}
		if len(reasoningRaw) > 0 {
			message.Meta = map[string]any{
				openAIReasoningItemsMetaKey: append([]string(nil), reasoningRaw...),
			}
		}
		translated.Content = append(translated.Content, message)
		pendingThinking = nil
		reasoningRaw = nil
	}
	for _, item := range resp.Output {
		switch actual := item.AsAny().(type) {
		case responses.ResponseReasoningItem:
			part, ok := translateReasoningItem(actual)
			if !ok {
				return nil, errors.New("openai: reasoning item has no summary or encrypted content")
			}
			part.Index = thinkingIndex
			thinkingIndex++
			pendingThinking = append(pendingThinking, part)
			reasoningRaw = append(reasoningRaw, actual.RawJSON())
		case responses.ResponseOutputMessage:
			message, err := translateAssistantMessage(actual, pendingThinking, reasoningRaw)
			if err != nil {
				return nil, err
			}
			translated.Content = append(translated.Content, message)
			pendingThinking = nil
			reasoningRaw = nil
		case responses.ResponseFunctionToolCall:
			flushThinking()
			if output != nil {
				return nil, fmt.Errorf("openai: structured output %q emitted tool calls", output.Name)
			}
			toolCall, err := translateToolCall(actual, codec)
			if err != nil {
				return nil, err
			}
			translated.Content = append(translated.Content, model.Message{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{model.ToolUsePart{
					ID:    toolCall.ID,
					Name:  string(toolCall.Name),
					Input: toolCall.Payload,
				}},
				Meta: map[string]any{
					openAIFunctionCallItemMetaKey:    actual.RawJSON(),
					openAIFunctionCallVersionMetaKey: openAIFunctionCallMetadataVersion2,
					openAIFunctionCallPayloadMetaKey: string(toolCall.Payload),
				},
			})
		default:
			return nil, fmt.Errorf("openai: unsupported response output item %T", actual)
		}
	}
	flushThinking()
	translated.StopReason = translateStopReason(resp, len(translated.ToolCalls()) > 0)
	translated.OutputLimited = openAIOutputLimited(resp)
	if output != nil {
		payload, err := structuredOutputPayload(translated.Content, output, outputProjection)
		if err != nil {
			return nil, err
		}
		translated.Content = replaceStructuredOutputText(translated.Content, string(payload))
	}
	return translated, nil
}

// preflightResponseSnapshot bounds the SDK response snapshot before
// translation copies provider-controlled output into model values.
func preflightResponseSnapshot(resp *responses.Response) error {
	raw := resp.RawJSON()
	if len(raw) > 16<<20 {
		return errors.New("openai: response snapshot exceeds 16777216 bytes")
	}
	values := len(resp.Output)
	for _, item := range resp.Output {
		switch actual := item.AsAny().(type) {
		case responses.ResponseReasoningItem:
			values += len(actual.Summary)
		case responses.ResponseOutputMessage:
			values += len(actual.Content)
			for _, content := range actual.Content {
				if text, ok := content.AsAny().(responses.ResponseOutputText); ok {
					values += len(text.Annotations)
				}
			}
		}
		if values > 100_000 {
			return errors.New("openai: response snapshot exceeds 100000 values")
		}
	}
	return nil
}

// openAIOutputLimited identifies an incomplete Responses API result whose
// generated output consumed the request's maximum output tokens.
func openAIOutputLimited(response *responses.Response) bool {
	return response.Status == responses.ResponseStatusIncomplete &&
		response.IncompleteDetails.Reason == openAIMaxOutputTokensReason
}

func translateAssistantMessage(
	message responses.ResponseOutputMessage,
	thinking []model.Part,
	reasoningRaw []string,
) (model.Message, error) {
	parts := make([]model.Part, 0, len(thinking)+len(message.Content))
	parts = append(parts, thinking...)
	for _, content := range message.Content {
		switch actual := content.AsAny().(type) {
		case responses.ResponseOutputText:
			part, err := translateTextContent(actual)
			if err != nil {
				return model.Message{}, err
			}
			parts = append(parts, part)
		case responses.ResponseOutputRefusal:
			parts = append(parts, model.TextPart{Text: actual.Refusal})
		default:
			return model.Message{}, fmt.Errorf("openai: unsupported assistant content item %T", actual)
		}
	}
	if len(parts) == 0 {
		return model.Message{}, errors.New("openai: assistant output message has no content")
	}
	meta := map[string]any{
		openAIOutputItemMetaKey: message.RawJSON(),
	}
	if len(reasoningRaw) > 0 {
		meta[openAIReasoningItemsMetaKey] = append([]string(nil), reasoningRaw...)
	}
	return model.Message{
		Role:  model.ConversationRoleAssistant,
		Parts: parts,
		Meta:  meta,
	}, nil
}

func translateTextContent(content responses.ResponseOutputText) (model.Part, error) {
	if len(content.Annotations) == 0 {
		return model.TextPart{Text: content.Text}, nil
	}
	citations, err := translateCitations(content.Annotations)
	if err != nil {
		return nil, err
	}
	return model.CitationsPart{
		Text:      content.Text,
		Citations: citations,
	}, nil
}

func translateReasoningItem(item responses.ResponseReasoningItem) (model.ThinkingPart, bool) {
	texts := make([]string, 0, len(item.Summary))
	for _, summary := range item.Summary {
		if summary.Text == "" {
			continue
		}
		texts = append(texts, summary.Text)
	}
	if len(texts) == 0 && item.EncryptedContent == "" {
		return model.ThinkingPart{}, false
	}
	part := model.ThinkingPart{
		Text:  strings.Join(texts, "\n"),
		Final: true,
	}
	if part.Text == "" && item.EncryptedContent != "" {
		part.Redacted = []byte(item.EncryptedContent)
	}
	return part, true
}

func translateCitations(annotations []responses.ResponseOutputTextAnnotationUnion) ([]model.Citation, error) {
	citations := make([]model.Citation, 0, len(annotations))
	for _, annotation := range annotations {
		switch actual := annotation.AsAny().(type) {
		case responses.ResponseOutputTextAnnotationFileCitation:
			citations = append(citations, model.Citation{
				Title:  actual.Filename,
				Source: actual.FileID,
			})
		case responses.ResponseOutputTextAnnotationURLCitation:
			citations = append(citations, model.Citation{
				Title:  actual.Title,
				Source: actual.URL,
			})
		case responses.ResponseOutputTextAnnotationContainerFileCitation:
			citations = append(citations, model.Citation{
				Title:  actual.Filename,
				Source: actual.FileID,
			})
		case responses.ResponseOutputTextAnnotationFilePath:
			citations = append(citations, model.Citation{
				Source: actual.FileID,
			})
		default:
			return nil, fmt.Errorf("openai: unsupported output text annotation %T", actual)
		}
	}
	return citations, nil
}

func translateToolCall(
	call responses.ResponseFunctionToolCall,
	codec *toolCodec,
) (model.ToolCall, error) {
	if call.CallID == "" {
		return model.ToolCall{}, errors.New("openai: tool call missing call_id")
	}
	if call.Name == "" {
		return model.ToolCall{}, fmt.Errorf("openai: tool call %q missing function name", call.CallID)
	}
	name, ok := codec.canonicalName(call.Name)
	if !ok {
		return model.ToolCall{}, fmt.Errorf(
			"openai: translate response tool call: %w",
			model.NewUnadvertisedToolNameError(call.Name),
		)
	}
	payload, err := decodeToolPayload(call.Arguments)
	if err != nil {
		return model.ToolCall{}, fmt.Errorf("openai: tool call %q payload: %w", call.CallID, err)
	}
	payload, err = codec.canonicalPayload(call.Name, payload)
	if err != nil {
		return model.ToolCall{}, fmt.Errorf("openai: tool call %q canonical payload: %w", call.CallID, err)
	}
	return model.ToolCall{
		Name:    tools.Ident(name),
		Payload: payload,
		ID:      call.CallID,
	}, nil
}

func translateStopReason(resp *responses.Response, hasToolCalls bool) string {
	switch resp.Status {
	case responses.ResponseStatusFailed:
		return string(resp.Status)
	case responses.ResponseStatusInProgress:
		return string(resp.Status)
	case responses.ResponseStatusQueued:
		return string(resp.Status)
	case responses.ResponseStatusIncomplete:
		if resp.IncompleteDetails.Reason != "" {
			return resp.IncompleteDetails.Reason
		}
		return string(resp.Status)
	case responses.ResponseStatusCancelled:
		return "cancelled"
	case responses.ResponseStatusCompleted:
		if hasToolCalls {
			return "tool_calls"
		}
		return "stop"
	default:
		if hasToolCalls {
			return "tool_calls"
		}
		return string(resp.Status)
	}
}

func structuredOutputPayload(
	content []model.Message,
	output *model.StructuredOutput,
	projection *strictSchemaProjection,
) (rawjson.Message, error) {
	if output == nil {
		return nil, nil
	}
	text := extractAssistantText(content)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("openai: structured output %q completed without content", output.Name)
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("openai: structured output %q payload is not valid JSON", structuredOutputName(output))
	}
	return projection.canonicalize([]byte(text))
}

// replaceStructuredOutputText keeps thinking and metadata but replaces all
// provider text fragments with one canonical structured-output document.
func replaceStructuredOutputText(content []model.Message, payload string) []model.Message {
	replaced := false
	for messageIndex := range content {
		if content[messageIndex].Role != model.ConversationRoleAssistant {
			continue
		}
		parts := content[messageIndex].Parts[:0]
		for _, part := range content[messageIndex].Parts {
			if _, ok := part.(model.TextPart); !ok {
				parts = append(parts, part)
				continue
			}
			if !replaced {
				parts = append(parts, model.TextPart{Text: payload})
				replaced = true
			}
		}
		content[messageIndex].Parts = parts
	}
	return content
}

func extractAssistantText(content []model.Message) string {
	var text strings.Builder
	for _, message := range content {
		if message.Role != model.ConversationRoleAssistant {
			continue
		}
		for _, part := range message.Parts {
			switch actual := part.(type) {
			case model.TextPart:
				text.WriteString(actual.Text)
			case model.CitationsPart:
				text.WriteString(actual.Text)
			}
		}
	}
	return text.String()
}

func translateUsage(usage responses.ResponseUsage, modelID string, modelClass model.ModelClass) model.TokenUsage {
	cacheReadTokens := int(usage.InputTokensDetails.CachedTokens)
	return model.TokenUsage{
		Model:           modelID,
		ModelClass:      modelClass,
		InputTokens:     int(usage.InputTokens),
		OutputTokens:    int(usage.OutputTokens),
		TotalTokens:     int(usage.TotalTokens),
		CacheReadTokens: cacheReadTokens,
	}
}

func chooseModelID(providerModel, resolvedModelID string) string {
	if providerModel != "" {
		return providerModel
	}
	return resolvedModelID
}

func decodeToolPayload(raw string) (rawjson.Message, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("tool payload is empty")
	}
	if !json.Valid([]byte(raw)) {
		return nil, errors.New("tool payload is not valid JSON")
	}
	return rawjson.Message([]byte(raw)), nil
}
