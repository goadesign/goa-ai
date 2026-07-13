// Package openai translates provider-neutral transcripts into OpenAI Responses
// API input items. This file preserves tool-loop fidelity by re-encoding prior
// assistant output items, function calls, and user tool results in
// provider-native form.
package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

const (
	openAIOutputItemMetaKey       = "openai_output_item"
	openAIFunctionCallItemMetaKey = "openai_function_call_item"
	openAIReasoningItemsMetaKey   = "openai_reasoning_items"
)

func encodeMessages(msgs []*model.Message, canonicalToProvider map[string]string) (responses.ResponseInputParam, error) {
	conversation := make(responses.ResponseInputParam, 0, len(msgs))
	sequence := 0
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		var (
			encoded []responses.ResponseInputItemUnionParam
			err     error
		)
		switch msg.Role {
		case model.ConversationRoleSystem:
			encoded, err = encodeSystemMessage(msg)
		case model.ConversationRoleUser:
			encoded, err = encodeUserMessage(msg, sequence)
		case model.ConversationRoleAssistant:
			encoded, err = encodeAssistantMessage(msg, canonicalToProvider, sequence)
		default:
			err = fmt.Errorf("openai: unsupported message role %q", msg.Role)
		}
		if err != nil {
			return nil, err
		}
		conversation = append(conversation, encoded...)
		sequence++
	}
	if len(conversation) == 0 {
		return nil, errors.New("openai: at least one input item is required")
	}
	return conversation, nil
}

func encodeSystemMessage(msg *model.Message) ([]responses.ResponseInputItemUnionParam, error) {
	text, err := collectTextParts("system", msg.Parts)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}
	return []responses.ResponseInputItemUnionParam{
		responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleSystem),
	}, nil
}

func encodeUserMessage(msg *model.Message, sequence int) ([]responses.ResponseInputItemUnionParam, error) {
	out := make([]responses.ResponseInputItemUnionParam, 0, len(msg.Parts))
	content := make(responses.ResponseInputMessageContentListParam, 0, len(msg.Parts))
	flushContent := func() {
		if len(content) == 0 {
			return
		}
		out = append(out, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleUser))
		content = nil
	}
	for _, part := range msg.Parts {
		switch actual := part.(type) {
		case model.TextPart:
			content = append(content, responses.ResponseInputContentUnionParam{
				OfInputText: &responses.ResponseInputTextParam{
					Text: actual.Text,
				},
			})
		case model.CitationsPart:
			return nil, errors.New("openai: replaying canonical citations without provider output metadata is not supported")
		case model.ImagePart:
			item, err := encodeImageContent(actual)
			if err != nil {
				return nil, err
			}
			content = append(content, item)
		case model.DocumentPart:
			item, err := encodeDocumentContent(actual)
			if err != nil {
				return nil, err
			}
			content = append(content, item)
		case model.ToolResultPart:
			flushContent()
			toolMessage, err := encodeToolResultMessage(actual, sequence, len(out))
			if err != nil {
				return nil, err
			}
			out = append(out, toolMessage)
		case model.CacheCheckpointPart:
			return nil, errors.New("openai: cache checkpoints are not supported")
		case model.ThinkingPart:
			return nil, fmt.Errorf("openai: unsupported user message part %T", part)
		default:
			return nil, fmt.Errorf("openai: unsupported user message part %T", part)
		}
	}
	flushContent()
	return out, nil
}

func encodeAssistantMessage(
	msg *model.Message,
	canonicalToProvider map[string]string,
	sequence int,
) ([]responses.ResponseInputItemUnionParam, error) {
	reusedReasoning, err := decodeReasoningItemsMeta(msg.Meta)
	if err != nil {
		return nil, err
	}
	reusedOutput, err := decodeOutputMessageMeta(msg.Meta)
	if err != nil {
		return nil, err
	}
	reusedFunctionCall, err := decodeFunctionCallMeta(msg.Meta)
	if err != nil {
		return nil, err
	}
	if reusedOutput != nil && reusedFunctionCall != nil {
		return nil, errors.New("openai: assistant message cannot carry output and function-call metadata")
	}
	var (
		reasoningParts []model.ThinkingPart
		visibleParts   []model.Part
		visibleText    strings.Builder
		toolUses       []model.ToolUsePart
		sawToolUse     bool
		sawCitations   bool
	)
	for _, part := range msg.Parts {
		switch actual := part.(type) {
		case model.TextPart:
			if sawToolUse {
				return nil, errors.New("openai: assistant text after tool_use is not representable")
			}
			visibleText.WriteString(actual.Text)
			visibleParts = append(visibleParts, actual)
		case model.CitationsPart:
			if sawToolUse {
				return nil, errors.New("openai: assistant text after tool_use is not representable")
			}
			visibleText.WriteString(actual.Text)
			visibleParts = append(visibleParts, actual)
			sawCitations = true
		case model.ThinkingPart:
			reasoningParts = append(reasoningParts, actual)
		case model.ToolUsePart:
			sawToolUse = true
			toolUses = append(toolUses, actual)
		case model.CacheCheckpointPart:
			return nil, errors.New("openai: cache checkpoints are not supported")
		default:
			return nil, fmt.Errorf("openai: unsupported assistant message part %T", part)
		}
	}

	out := make([]responses.ResponseInputItemUnionParam, 0, len(reusedReasoning)+len(toolUses)+1)
	if len(reusedReasoning) > 0 {
		for _, item := range reusedReasoning {
			itemCopy := item
			out = append(out, responses.ResponseInputItemUnionParam{
				OfReasoning: &itemCopy,
			})
		}
	} else if len(reasoningParts) > 0 {
		return nil, errors.New("openai: thinking replay requires provider reasoning metadata")
	}

	if reusedOutput != nil {
		if err := validateOutputMessageAgreement(msg.Meta, visibleParts); err != nil {
			return nil, err
		}
		out = append(out, responses.ResponseInputItemUnionParam{
			OfOutputMessage: reusedOutput,
		})
	} else if visibleText.Len() > 0 {
		if sawCitations {
			return nil, errors.New("openai: replaying canonical citations requires provider output metadata")
		}
		out = append(out, responses.ResponseInputItemUnionParam{
			OfOutputMessage: &responses.ResponseOutputMessageParam{
				ID:     syntheticID("assistant_message", sequence, 0),
				Status: responses.ResponseOutputMessageStatusCompleted,
				Content: []responses.ResponseOutputMessageContentUnionParam{{
					OfOutputText: &responses.ResponseOutputTextParam{
						Text: visibleText.String(),
					},
				}},
			},
		})
	}

	if reusedFunctionCall != nil {
		if len(toolUses) != 1 {
			return nil, errors.New("openai: function-call metadata requires exactly one tool_use part")
		}
		if err := validateFunctionCallAgreement(*reusedFunctionCall, toolUses[0], canonicalToProvider); err != nil {
			return nil, err
		}
		out = append(out, responses.ResponseInputItemUnionParam{
			OfFunctionCall: reusedFunctionCall,
		})
		toolUses = nil
	}
	for index, part := range toolUses {
		call, err := encodeToolUse(part, canonicalToProvider, sequence, index)
		if err != nil {
			return nil, err
		}
		out = append(out, responses.ResponseInputItemUnionParam{
			OfFunctionCall: &call,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func collectTextParts(role string, parts []model.Part) (string, error) {
	var text strings.Builder
	for _, part := range parts {
		switch actual := part.(type) {
		case model.TextPart:
			text.WriteString(actual.Text)
		case model.CitationsPart:
			return "", fmt.Errorf("openai: replaying canonical citations in %s messages is not supported", role)
		case model.CacheCheckpointPart:
			return "", errors.New("openai: cache checkpoints are not supported")
		case model.ThinkingPart:
			return "", fmt.Errorf("openai: unsupported %s message part %T", role, part)
		default:
			return "", fmt.Errorf("openai: unsupported %s message part %T", role, part)
		}
	}
	return text.String(), nil
}

func encodeImageContent(part model.ImagePart) (responses.ResponseInputContentUnionParam, error) {
	if len(part.Bytes) == 0 {
		return responses.ResponseInputContentUnionParam{}, errors.New("openai: image part missing bytes")
	}
	if part.Format == "" {
		return responses.ResponseInputContentUnionParam{}, errors.New("openai: image part missing format")
	}
	mime := string(part.Format)
	if !strings.Contains(mime, "/") {
		mime = "image/" + mime
	}
	return responses.ResponseInputContentUnionParam{
		OfInputImage: &responses.ResponseInputImageParam{
			Detail:   responses.ResponseInputImageDetailAuto,
			ImageURL: param.NewOpt(fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(part.Bytes))),
		},
	}, nil
}

func encodeDocumentContent(part model.DocumentPart) (responses.ResponseInputContentUnionParam, error) {
	if part.Context != "" {
		return responses.ResponseInputContentUnionParam{}, errors.New("openai: document context is not supported")
	}
	if part.Cite {
		return responses.ResponseInputContentUnionParam{}, errors.New("openai: document citations are not supported")
	}
	file := &responses.ResponseInputFileParam{}
	if part.URI != "" {
		if err := validateDocumentURI(part.URI); err != nil {
			return responses.ResponseInputContentUnionParam{}, err
		}
		file.FileURL = param.NewOpt(part.URI)
	} else {
		payload, err := documentBytes(part)
		if err != nil {
			return responses.ResponseInputContentUnionParam{}, err
		}
		file.FileData = param.NewOpt(base64.StdEncoding.EncodeToString(payload))
	}
	if name := documentFilename(part); name != "" {
		file.Filename = param.NewOpt(name)
	}
	return responses.ResponseInputContentUnionParam{OfInputFile: file}, nil
}

func documentBytes(part model.DocumentPart) ([]byte, error) {
	switch {
	case len(part.Bytes) > 0:
		return part.Bytes, nil
	case part.Text != "":
		return []byte(part.Text), nil
	case len(part.Chunks) > 0:
		return []byte(strings.Join(part.Chunks, "\n\n")), nil
	default:
		return nil, errors.New("openai: document part must provide bytes, text, chunks, or uri")
	}
}

func documentFilename(part model.DocumentPart) string {
	name := part.Name
	if name == "" && part.Format == "" {
		return ""
	}
	if name == "" {
		return "document." + string(part.Format)
	}
	if part.Format == "" || strings.Contains(name, ".") {
		return name
	}
	return name + "." + string(part.Format)
}

func validateDocumentURI(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("openai: invalid document uri %q: %w", raw, err)
	}
	switch parsed.Scheme {
	case "http", "https", "data":
		return nil
	default:
		return fmt.Errorf("openai: document uri scheme %q is not supported", parsed.Scheme)
	}
}

// encodeToolUse converts a provider-neutral assistant tool_use declaration into
// an OpenAI function-call item.
func encodeToolUse(
	part model.ToolUsePart,
	canonicalToProvider map[string]string,
	sequence int,
	index int,
) (responses.ResponseFunctionToolCallParam, error) {
	if part.ID == "" {
		return responses.ResponseFunctionToolCallParam{}, errors.New("openai: tool_use part missing id")
	}
	if part.Name == "" {
		return responses.ResponseFunctionToolCallParam{}, errors.New("openai: tool_use part missing name")
	}
	providerName, ok := canonicalToProvider[part.Name]
	if !ok {
		for canonical, provider := range canonicalToProvider {
			if provider == part.Name {
				return responses.ResponseFunctionToolCallParam{}, fmt.Errorf(
					"openai: historical provider tool name %q collides with current tool %q",
					part.Name,
					canonical,
				)
			}
		}
		providerName = part.Name
	}
	payload, err := marshalToolInput(part.Input)
	if err != nil {
		return responses.ResponseFunctionToolCallParam{}, fmt.Errorf(
			"openai: tool_use %q payload: %w",
			part.Name,
			err,
		)
	}
	return responses.ResponseFunctionToolCallParam{
		Arguments: payload,
		CallID:    part.ID,
		ID:        param.NewOpt(syntheticID("tool_call", sequence, index)),
		Name:      providerName,
		Status:    responses.ResponseFunctionToolCallStatusCompleted,
	}, nil
}

// encodeToolResultMessage converts a provider-neutral tool result into the
// OpenAI function_call_output item expected after assistant function calls.
func encodeToolResultMessage(part model.ToolResultPart, sequence int, index int) (responses.ResponseInputItemUnionParam, error) {
	if part.ToolUseID == "" {
		return responses.ResponseInputItemUnionParam{}, errors.New("openai: tool_result part missing tool use id")
	}
	content, err := encodeToolResultMessageContent(part)
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, fmt.Errorf("openai: tool_result %q: %w", part.ToolUseID, err)
	}
	return responses.ResponseInputItemUnionParam{
		OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: part.ToolUseID,
			ID:     param.NewOpt(syntheticID("tool_result", sequence, index)),
			Output: content,
		},
	}, nil
}

// encodeToolResultMessageContent preserves explicit tool failure semantics even
// though OpenAI function_call_output items only accept string content.
func encodeToolResultMessageContent(part model.ToolResultPart) (string, error) {
	if !part.IsError {
		return encodeToolResultContent(part.Content)
	}
	text, err := encodeToolResultErrorText(part.Content)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(map[string]any{
		"is_error": true,
		"error":    text,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func encodeToolResultErrorText(content any) (string, error) {
	switch actual := content.(type) {
	case string:
		return actual, nil
	case []byte:
		return string(actual), nil
	default:
		return "", fmt.Errorf("tool_result errors must carry plain text, got %T", content)
	}
}

func encodeToolResultContent(content any) (string, error) {
	switch actual := content.(type) {
	case nil:
		return "", nil
	case string:
		return actual, nil
	case []byte:
		return string(actual), nil
	case json.RawMessage:
		return string(actual), nil
	case rawjson.Message:
		return string(actual), nil
	default:
		data, err := json.Marshal(actual)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

func decodeOutputMessageMeta(meta map[string]any) (*responses.ResponseOutputMessageParam, error) {
	raw, err := metaString(meta, openAIOutputItemMetaKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var item responses.ResponseOutputMessageParam
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil, fmt.Errorf("openai: invalid assistant output metadata: %w", err)
	}
	return &item, nil
}

func decodeFunctionCallMeta(meta map[string]any) (*responses.ResponseFunctionToolCallParam, error) {
	raw, err := metaString(meta, openAIFunctionCallItemMetaKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var item responses.ResponseFunctionToolCallParam
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil, fmt.Errorf("openai: invalid function-call metadata: %w", err)
	}
	return &item, nil
}

// validateOutputMessageAgreement rejects replay when canonical visible parts
// diverge from the provider output item retained for lossless replay.
func validateOutputMessageAgreement(meta map[string]any, visibleParts []model.Part) error {
	raw, err := metaString(meta, openAIOutputItemMetaKey)
	if err != nil {
		return err
	}
	var output responses.ResponseOutputMessage
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		return fmt.Errorf("openai: invalid assistant output metadata: %w", err)
	}
	translated, err := translateAssistantMessage(output, nil, nil)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(translated.Parts, visibleParts) {
		return errors.New("openai: assistant parts diverged from provider output metadata")
	}
	return nil
}

// validateFunctionCallAgreement rejects replay when a canonical tool use
// diverges from its provider-authored function-call envelope.
func validateFunctionCallAgreement(
	item responses.ResponseFunctionToolCallParam,
	part model.ToolUsePart,
	canonicalToProvider map[string]string,
) error {
	providerName, ok := canonicalToProvider[part.Name]
	if !ok {
		providerName = part.Name
	}
	if item.CallID != part.ID || item.Name != providerName || item.Arguments != string(part.Input) {
		return errors.New("openai: tool_use diverged from provider function-call metadata")
	}
	return nil
}

func decodeReasoningItemsMeta(meta map[string]any) ([]responses.ResponseReasoningItemParam, error) {
	rawItems, err := metaStrings(meta, openAIReasoningItemsMetaKey)
	if err != nil {
		return nil, err
	}
	if len(rawItems) == 0 {
		return nil, nil
	}
	items := make([]responses.ResponseReasoningItemParam, 0, len(rawItems))
	for _, raw := range rawItems {
		var item responses.ResponseReasoningItemParam
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, fmt.Errorf("openai: invalid reasoning metadata: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

func metaString(meta map[string]any, key string) (string, error) {
	if meta == nil {
		return "", nil
	}
	value, ok := meta[key]
	if !ok {
		return "", nil
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	return "", fmt.Errorf("openai: metadata %q must be a string, got %T", key, value)
}

func metaStrings(meta map[string]any, key string) ([]string, error) {
	if meta == nil {
		return nil, nil
	}
	value, ok := meta[key]
	if !ok {
		return nil, nil
	}
	switch actual := value.(type) {
	case string:
		return []string{actual}, nil
	case []string:
		return append([]string(nil), actual...), nil
	case []any:
		out := make([]string, 0, len(actual))
		for index, item := range actual {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("openai: metadata %q item %d must be a string, got %T", key, index, item)
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("openai: metadata %q must be a string array, got %T", key, value)
	}
}

func syntheticID(prefix string, sequence int, index int) string {
	return fmt.Sprintf("%s_%d_%d", prefix, sequence, index)
}

func marshalToolInput(input rawjson.Message) (string, error) {
	if !json.Valid(input) {
		return "", errors.New("tool input is not valid JSON")
	}
	return string(input), nil
}
