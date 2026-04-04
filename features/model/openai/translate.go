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

func translateResponse(
	resp *responses.Response,
	providerToCanonical map[string]string,
	resolvedModelID string,
	resolvedModelClass model.ModelClass,
	output *model.StructuredOutput,
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
	translated := &model.Response{
		Usage: translateUsage(resp.Usage, chooseModelID(resp.Model, resolvedModelID), resolvedModelClass),
	}
	var (
		pendingThinking []model.Part
		reasoningRaw    []string
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
				continue
			}
			pendingThinking = append(pendingThinking, part)
			reasoningRaw = append(reasoningRaw, actual.RawJSON())
		case responses.ResponseOutputMessage:
			message, ok, err := translateAssistantMessage(actual, pendingThinking, reasoningRaw)
			if err != nil {
				return nil, err
			}
			if ok {
				translated.Content = append(translated.Content, message)
			}
			pendingThinking = nil
			reasoningRaw = nil
		case responses.ResponseFunctionToolCall:
			flushThinking()
			if output != nil {
				return nil, fmt.Errorf("openai: structured output %q emitted tool calls", output.Name)
			}
			toolCall, err := translateToolCall(actual, providerToCanonical)
			if err != nil {
				return nil, err
			}
			translated.ToolCalls = append(translated.ToolCalls, toolCall)
		default:
			return nil, fmt.Errorf("openai: unsupported response output item %T", actual)
		}
	}
	flushThinking()
	translated.StopReason = translateStopReason(resp, len(translated.ToolCalls) > 0)
	if output != nil {
		if _, err := structuredOutputPayload(translated.Content, output); err != nil {
			return nil, err
		}
	}
	return translated, nil
}

func translateAssistantMessage(
	message responses.ResponseOutputMessage,
	thinking []model.Part,
	reasoningRaw []string,
) (model.Message, bool, error) {
	parts := make([]model.Part, 0, len(thinking)+len(message.Content))
	parts = append(parts, thinking...)
	for _, content := range message.Content {
		switch actual := content.AsAny().(type) {
		case responses.ResponseOutputText:
			parts = append(parts, translateTextContent(actual))
		case responses.ResponseOutputRefusal:
			parts = append(parts, model.TextPart{Text: actual.Refusal})
		default:
			return model.Message{}, false, fmt.Errorf("openai: unsupported assistant content item %T", actual)
		}
	}
	if len(parts) == 0 {
		return model.Message{}, false, nil
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
	}, true, nil
}

func translateTextContent(content responses.ResponseOutputText) model.Part {
	if len(content.Annotations) == 0 {
		return model.TextPart{Text: content.Text}
	}
	return model.CitationsPart{
		Text:      content.Text,
		Citations: translateCitations(content.Annotations),
	}
}

func translateReasoningItem(item responses.ResponseReasoningItem) (model.Part, bool) {
	texts := make([]string, 0, len(item.Summary))
	for _, summary := range item.Summary {
		if summary.Text == "" {
			continue
		}
		texts = append(texts, summary.Text)
	}
	if len(texts) == 0 && item.EncryptedContent == "" {
		return nil, false
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

func translateCitations(annotations []responses.ResponseOutputTextAnnotationUnion) []model.Citation {
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
			continue
		}
	}
	return citations
}

func translateToolCall(
	call responses.ResponseFunctionToolCall,
	providerToCanonical map[string]string,
) (model.ToolCall, error) {
	if call.CallID == "" {
		return model.ToolCall{}, errors.New("openai: tool call missing call_id")
	}
	if call.Name == "" {
		return model.ToolCall{}, fmt.Errorf("openai: tool call %q missing function name", call.CallID)
	}
	payload, err := decodeToolPayload(call.Arguments)
	if err != nil {
		return model.ToolCall{}, fmt.Errorf("openai: tool call %q payload: %w", call.CallID, err)
	}
	name := call.Name
	if canonical, ok := providerToCanonical[name]; ok {
		name = canonical
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

func structuredOutputPayload(content []model.Message, output *model.StructuredOutput) (rawjson.Message, error) {
	if output == nil {
		return nil, nil
	}
	text := strings.TrimSpace(extractAssistantText(content))
	if text == "" {
		return nil, fmt.Errorf("openai: structured output %q completed without content", output.Name)
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("openai: structured output %q payload is not valid JSON", structuredOutputName(output))
	}
	return rawjson.Message([]byte(text)), nil
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
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 && cacheReadTokens == 0 {
		return model.TokenUsage{}
	}
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
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return rawjson.Message([]byte("{}")), nil
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, errors.New("tool payload is not valid JSON")
	}
	return rawjson.Message([]byte(trimmed)), nil
}
