// Package openai provides a model.Client implementation backed by the OpenAI
// Chat Completions API. It translates goa-ai requests into ChatCompletion
// calls using github.com/sashabaranov/go-openai and maps responses back to the
// generic planner structures.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	openai "github.com/sashabaranov/go-openai"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

// ChatClient captures the subset of the go-openai client used by the adapter.
type ChatClient interface {
	CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (
		openai.ChatCompletionResponse, error)
}

// Options configures the OpenAI adapter.
type Options struct {
	Client       ChatClient
	DefaultModel string
}

// Client implements model.Client via the OpenAI Chat Completions API.
type Client struct {
	chat  ChatClient
	model string
}

// New builds an OpenAI-backed model client from the provided options.
func New(opts Options) (*Client, error) {
	if opts.Client == nil {
		return nil, errors.New("openai client is required")
	}
	modelID := opts.DefaultModel
	if modelID == "" {
		return nil, errors.New("default model is required")
	}
	return &Client{chat: opts.Client, model: modelID}, nil
}

// NewFromAPIKey constructs a client using the default go-openai HTTP client.
func NewFromAPIKey(apiKey, defaultModel string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("api key is required")
	}
	return New(Options{Client: openai.NewClient(apiKey), DefaultModel: defaultModel})
}

// Complete renders a chat completion using the configured OpenAI client.
func (c *Client) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if len(req.Messages) == 0 {
		return model.Response{}, errors.New("messages are required")
	}
	modelID := req.Model
	if modelID == "" {
		modelID = c.model
	}
	messages := make([]openai.ChatCompletionMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m == nil {
			continue
		}
		// Join only text parts for OpenAI; ignore tool_result parts.
		var text string
		for _, p := range m.Parts {
			if tp, ok := p.(model.TextPart); ok && tp.Text != "" {
				text += tp.Text
			}
		}
		// If JSON-only requested, prepend a strict instruction.
		if req.ResponseFormat != nil {
			if req.ResponseFormat.JSONSchema != nil || req.ResponseFormat.JSONOnly {
				text = "Return only a single valid JSON value with no extra text.\n" + text
			}
		}
		messages = append(messages, openai.ChatCompletionMessage{Role: string(m.Role), Content: text})
	}
	tools, err := encodeTools(req.Tools)
	if err != nil {
		return model.Response{}, err
	}
	request := openai.ChatCompletionRequest{
		Model:       modelID,
		Messages:    messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       tools,
	}
	// Map ResponseFormat to OpenAI JSON mode when requested.
	if rf := req.ResponseFormat; rf != nil {
		if len(rf.JSONSchema) > 0 {
			name := rf.SchemaName
			if name == "" {
				name = "result"
			}
			request.ResponseFormat = &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
				JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
					Name:   name,
					Schema: rf.JSONSchema,
					Strict: true,
				},
			}
		} else if rf.JSONOnly {
			request.ResponseFormat = &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			}
		}
	}
	response, err := c.chat.CreateChatCompletion(ctx, request)
	if err != nil {
		return model.Response{}, fmt.Errorf("openai chat completion: %w", err)
	}
	return translateResponse(response), nil
}

// Stream reports that OpenAI Chat Completions streaming is not yet supported by
// this adapter. Callers should fall back to Complete.
func (c *Client) Stream(context.Context, model.Request) (model.Streamer, error) {
	return nil, model.ErrStreamingUnsupported
}

func encodeTools(defs []*model.ToolDefinition) ([]openai.Tool, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	tools := make([]openai.Tool, 0, len(defs))
	for _, def := range defs {
		if def == nil {
			continue
		}
		params, err := json.Marshal(def.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal tool %s schema: %w", def.Name, err)
		}
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  json.RawMessage(params),
			},
		})
	}
	return tools, nil
}

func translateResponse(resp openai.ChatCompletionResponse) model.Response {
	messages := make([]model.Message, 0, len(resp.Choices))
	toolCalls := make([]model.ToolCall, 0)
	for _, choice := range resp.Choices {
		msg := choice.Message
		if msg.Content != "" {
			messages = append(messages, model.Message{Role: model.ConversationRole(msg.Role), Parts: []model.Part{model.TextPart{Text: msg.Content}}})
		}
		for _, call := range msg.ToolCalls {
			payload := parseToolArguments(call.Function.Arguments)
			toolCalls = append(toolCalls, model.ToolCall{
				Name:    tools.Ident(call.Function.Name),
				Payload: payload,
			})
		}
	}
	usage := model.TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}
	stop := ""
	if len(resp.Choices) > 0 {
		stop = string(resp.Choices[0].FinishReason)
	}
	return model.Response{
		Content:    messages,
		ToolCalls:  toolCalls,
		Usage:      usage,
		StopReason: stop,
	}
}

func parseToolArguments(raw string) any {
	if raw == "" {
		return nil
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return map[string]any{"raw": raw}
	}
	return payload
}
