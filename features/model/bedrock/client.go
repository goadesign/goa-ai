// Package bedrock provides a model.Client implementation backed by the AWS
// Bedrock Converse API. It mirrors the inference-engine request pipeline used
// in production systems: split system vs. conversational messages, encode tool
// schemas into Bedrock's ToolConfiguration, and translate Converse responses
// (text + tool_use blocks) back into planner-friendly structures.
package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	defaultMaxTokens      = 4096
	defaultThinkingBudget = 16384
)

// RuntimeClient mirrors the subset of the AWS Bedrock runtime client required
// by the adapter. It matches *bedrockruntime.Client so callers can pass either
// the real client or a mock in tests.
type RuntimeClient interface {
	Converse(ctx context.Context, params *bedrockruntime.ConverseInput,
		optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput,
		optFns ...func(*bedrockruntime.Options)) (StreamOutput, error)
}

// StreamOutput is the subset of the AWS ConverseStream output type required by
// the adapter. It is satisfied by *bedrockruntime.ConverseStreamOutput and
// simplifies unit testing by allowing fake implementations.
type StreamOutput interface {
	GetStream() *bedrockruntime.ConverseStreamEventStream
}

// Options configures the Bedrock client adapter.
type Options struct {
	// Runtime provides access to the Bedrock runtime. Required.
	Runtime RuntimeClient
	// Model is the default model identifier (e.g., "anthropic.claude-3-sonnet").
	// Requests can override this per-call via Request.Model.
	Model string
	// MaxTokens sets the default completion cap when a request does not specify
	// MaxTokens. Must be positive; defaults to 4096.
	MaxTokens int
	// Temperature is used when a request does not specify Temperature.
	Temperature float32
	// ThinkingBudget defines the thinking token budget when thinking is enabled
	// for streaming calls. Defaults to 16k tokens to match the production
	// inference-engine settings.
	ThinkingBudget int
}

// Client implements model.Client on top of AWS Bedrock Converse.
type Client struct {
	runtime RuntimeClient
	model   string
	maxTok  int
	temp    float32
	think   int
}

type requestParts struct {
	modelID    string
	messages   []brtypes.Message
	system     []brtypes.SystemContentBlock
	toolConfig *brtypes.ToolConfiguration
}

type thinkingConfig struct {
	enable bool
	budget int
}

// New constructs a Bedrock-backed model client using the provided options.
func New(opts Options) (*Client, error) {
	if opts.Runtime == nil {
		return nil, errors.New("bedrock runtime client is required")
	}
	modelID := strings.TrimSpace(opts.Model)
	if modelID == "" {
		return nil, errors.New("model identifier is required")
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	thinkBudget := opts.ThinkingBudget
	if thinkBudget <= 0 {
		thinkBudget = defaultThinkingBudget
	}
	return &Client{
		runtime: opts.Runtime,
		model:   modelID,
		maxTok:  maxTokens,
		temp:    opts.Temperature,
		think:   thinkBudget,
	}, nil
}

// Complete issues a chat completion request to the configured Bedrock model
// using the Converse API and translates the response into planner-friendly
// structures (assistant messages + tool calls).
func (c *Client) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	parts, err := c.prepareRequest(req)
	if err != nil {
		return model.Response{}, err
	}
	output, err := c.runtime.Converse(ctx, c.buildConverseInput(parts, req))
	if err != nil {
		return model.Response{}, fmt.Errorf("bedrock converse: %w", err)
	}
	return translateResponse(output)
}

// Stream invokes the Bedrock ConverseStream API and adapts incremental events
// into model.Chunks so planners can surface partial responses.
func (c *Client) Stream(ctx context.Context, req model.Request) (model.Streamer, error) {
	parts, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	thinking := c.resolveThinking(req, parts)
	input := c.buildConverseStreamInput(parts, req, thinking)
	out, err := c.runtime.ConverseStream(ctx, input, c.streamOptions(thinking)...)
	if err != nil {
		return nil, fmt.Errorf("bedrock converse stream: %w", err)
	}
	stream := out.GetStream()
	if stream == nil {
		return nil, errors.New("bedrock: stream output missing event stream")
	}
	return newBedrockStreamer(ctx, stream), nil
}

func (c *Client) prepareRequest(req model.Request) (*requestParts, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("bedrock: messages are required")
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = c.model
	}
	messages, system, err := splitMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	toolConfig, err := encodeTools(req.Tools)
	if err != nil {
		return nil, err
	}
	return &requestParts{
		modelID:    modelID,
		messages:   messages,
		system:     system,
		toolConfig: toolConfig,
	}, nil
}

func (c *Client) buildConverseInput(parts *requestParts, req model.Request) *bedrockruntime.ConverseInput {
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(parts.modelID),
		Messages: parts.messages,
	}
	if len(parts.system) > 0 {
		input.System = parts.system
	}
	if parts.toolConfig != nil {
		input.ToolConfig = parts.toolConfig
	}
	if cfg := c.inferenceConfig(req.MaxTokens, req.Temperature); cfg != nil {
		input.InferenceConfig = cfg
	}
	return input
}

func (c *Client) buildConverseStreamInput(
	parts *requestParts, req model.Request, thinking thinkingConfig,
) *bedrockruntime.ConverseStreamInput {
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(parts.modelID),
		Messages: parts.messages,
	}
	if len(parts.system) > 0 {
		input.System = parts.system
	}
	if parts.toolConfig != nil {
		input.ToolConfig = parts.toolConfig
	}
	if thinking.enable {
		input.AdditionalModelRequestFields = document.NewLazyDocument(&map[string]any{
			"thinking": map[string]any{
				"type":          "enabled",
				"budget_tokens": thinking.budget,
			},
		})
	}
	if cfg := c.inferenceConfig(req.MaxTokens, req.Temperature); cfg != nil {
		input.InferenceConfig = cfg
	}
	return input
}

func (c *Client) resolveThinking(req model.Request, parts *requestParts) thinkingConfig {
	if req.Thinking == nil || !req.Thinking.Enable {
		return thinkingConfig{}
	}
	if parts.toolConfig == nil {
		return thinkingConfig{}
	}
	budget := req.Thinking.BudgetTokens
	if budget <= 0 {
		budget = c.think
	}
	if budget <= 0 {
		budget = defaultThinkingBudget
	}
	return thinkingConfig{enable: true, budget: budget}
}

func (c *Client) streamOptions(thinking thinkingConfig) []func(*bedrockruntime.Options) {
	if !thinking.enable {
		return nil
	}
	return []func(*bedrockruntime.Options){
		bedrockruntime.WithAPIOptions(
			smithyhttp.AddHeaderValue("x-amzn-bedrock-beta", "interleaved-thinking-2025-05-14"),
		),
	}
}

func (c *Client) inferenceConfig(maxTokens int, temp float32) *brtypes.InferenceConfiguration {
	var cfg brtypes.InferenceConfiguration
	tokens := c.effectiveMaxTokens(maxTokens)
	if tokens > 0 {
		cfg.MaxTokens = aws.Int32(int32(tokens)) //nolint:gosec // AWS SDK requires int32
	}
	if t := c.effectiveTemperature(temp); t > 0 {
		cfg.Temperature = aws.Float32(t)
	}
	if cfg.MaxTokens == nil && cfg.Temperature == nil {
		return nil
	}
	return &cfg
}

func (c *Client) effectiveMaxTokens(requested int) int {
	if requested > 0 {
		return requested
	}
	return c.maxTok
}

func (c *Client) effectiveTemperature(requested float32) float32 {
	if requested > 0 {
		return requested
	}
	return c.temp
}

func splitMessages(msgs []*model.Message) ([]brtypes.Message, []brtypes.SystemContentBlock, error) {
	var (
		conversation []brtypes.Message
		system       []brtypes.SystemContentBlock
	)
	for _, m := range msgs {
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		switch strings.ToLower(m.Role) {
		case "system":
			system = append(system, &brtypes.SystemContentBlockMemberText{Value: text})
		case "assistant":
			conversation = append(conversation, brtypes.Message{
				Role:    brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: text}},
			})
		default:
			conversation = append(conversation, brtypes.Message{
				Role:    brtypes.ConversationRoleUser,
				Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: text}},
			})
		}
	}
	if len(conversation) == 0 {
		return nil, nil, errors.New("bedrock: at least one user/assistant message is required")
	}
	return conversation, system, nil
}

func encodeTools(defs []*model.ToolDefinition) (*brtypes.ToolConfiguration, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	tools := make([]brtypes.Tool, 0, len(defs))
	for _, def := range defs {
		if def == nil {
			continue
		}
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		schemaDoc, err := toDocument(def.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("bedrock tool %s schema: %w", def.Name, err)
		}
		spec := brtypes.ToolSpecification{
			Name:        aws.String(name),
			Description: aws.String(def.Description),
			InputSchema: &brtypes.ToolInputSchemaMemberJson{Value: schemaDoc},
		}
		tools = append(tools, &brtypes.ToolMemberToolSpec{Value: spec})
	}
	if len(tools) == 0 {
		return nil, nil
	}
	return &brtypes.ToolConfiguration{Tools: tools}, nil
}

func toDocument(schema any) (document.Interface, error) {
	if schema == nil {
		m := map[string]any{"type": "object"}
		return lazyDocument(m), nil
	}
	switch v := schema.(type) {
	case document.Interface:
		return v, nil
	case json.RawMessage:
		var decoded any
		if len(v) == 0 {
			return lazyDocument(map[string]any{"type": "object"}), nil
		}
		if err := json.Unmarshal(v, &decoded); err != nil {
			return nil, err
		}
		return lazyDocument(decoded), nil
	default:
		return lazyDocument(v), nil
	}
}

func translateResponse(output *bedrockruntime.ConverseOutput) (model.Response, error) {
	if output == nil {
		return model.Response{}, errors.New("bedrock: response is nil")
	}
	var resp model.Response
	if msg, ok := output.Output.(*brtypes.ConverseOutputMemberMessage); ok {
		for _, block := range msg.Value.Content {
			switch v := block.(type) {
			case *brtypes.ContentBlockMemberText:
				if strings.TrimSpace(v.Value) == "" {
					continue
				}
				resp.Content = append(resp.Content, model.Message{Role: "assistant", Content: v.Value})
			case *brtypes.ContentBlockMemberToolUse:
				payload := decodeDocument(v.Value.Input)
				name := ""
				if v.Value.Name != nil {
					name = *v.Value.Name
				}
				resp.ToolCalls = append(resp.ToolCalls, model.ToolCall{Name: tools.Ident(name), Payload: payload})
			}
		}
	}
	if usage := output.Usage; usage != nil {
		resp.Usage = model.TokenUsage{
			InputTokens:  int(ptrValue(usage.InputTokens)),
			OutputTokens: int(ptrValue(usage.OutputTokens)),
			TotalTokens:  int(ptrValue(usage.TotalTokens)),
		}
	}
	resp.StopReason = string(output.StopReason)
	return resp, nil
}

func decodeDocument(doc document.Interface) any {
	if doc == nil {
		return nil
	}
	data, err := doc.MarshalSmithyDocument()
	if err != nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	return value
}

func ptrValue[T ~int32 | ~int64](ptr *T) T {
	if ptr == nil {
		return 0
	}
	return *ptr
}

func lazyDocument(v any) document.Interface {
	wrapped := v
	return document.NewLazyDocument(&wrapped)
}
