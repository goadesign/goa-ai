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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"goa.design/clue/log"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const (
	defaultMaxTokens      = 4096
	defaultThinkingBudget = 16384
)

// RuntimeClient mirrors the subset of the AWS Bedrock runtime client required
// by the adapter. It matches *bedrockruntime.Client so callers can pass either
// the real client or a mock in tests.
type RuntimeClient interface {
	Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error)
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

	// DefaultModel is the default model identifier (e.g., Sonnet).
	DefaultModel string

	// HighModel is the high-reasoning model identifier (e.g., Opus/Sonnet-Reasoning).
	HighModel string

	// SmallModel is the small/cheap model identifier (e.g., Haiku).
	SmallModel string

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
	runtime      RuntimeClient
	defaultModel string
	highModel    string
	smallModel   string
	maxTok       int
	temp         float32
	think        int
	ledger       ledgerSource
}

// ledgerSource provides provider-ready messages for a given run when available.
// This interface is internal to the goa-ai bedrock client and implemented by
// the runtime using engine-specific mechanisms (e.g., Temporal workflow queries).
type ledgerSource interface {
	Messages(ctx context.Context, runID string) ([]*model.Message, error)
}

type requestParts struct {
	modelID    string
	messages   []brtypes.Message
	system     []brtypes.SystemContentBlock
	toolConfig *brtypes.ToolConfiguration
}

type thinkingConfig struct {
	enable      bool
	interleaved bool
	budget      int
}

// New constructs a Bedrock-backed model client using the provided options.
func New(opts Options) (*Client, error) {
	if opts.Runtime == nil {
		return nil, errors.New("bedrock runtime client is required")
	}
	// DefaultModel should be provided; High/Small are optional but recommended.
	if opts.DefaultModel == "" {
		return nil, errors.New("default model identifier is required")
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
		runtime:      opts.Runtime,
		defaultModel: opts.DefaultModel,
		highModel:    opts.HighModel,
		smallModel:   opts.SmallModel,
		maxTok:       maxTokens,
		temp:         opts.Temperature,
		think:        thinkBudget,
	}, nil
}

// NewWithLedger constructs a Bedrock-backed model client with an internal ledger
// source for transcript rehydration. The ledger source is used to prepend provider-
// ready messages for the given RunID before encoding the request.
func NewWithLedger(aws *bedrockruntime.Client, opts Options, ledger ledgerSource) (*Client, error) {
	opts.Runtime = aws
	c, err := New(opts)
	if err != nil {
		return nil, err
	}
	c.ledger = ledger
	return c, nil
}

// Complete issues a chat completion request to the configured Bedrock model
// using the Converse API and translates the response into planner-friendly
// structures (assistant messages + tool calls).
func (c *Client) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	parts, err := c.prepareRequest(ctx, req)
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
	parts, err := c.prepareRequest(ctx, req)
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

func (c *Client) prepareRequest(ctx context.Context, req model.Request) (*requestParts, error) {
	// Rehydrate provider-ready messages from the ledger when a RunID is provided.
	var merged []*model.Message
	if c.ledger != nil && req.RunID != "" {
		if msgs, err := c.ledger.Messages(ctx, req.RunID); err == nil && len(msgs) > 0 {
			merged = append(merged, msgs...)
		}
	}
	if len(req.Messages) > 0 {
		merged = append(merged, req.Messages...)
	}
	if len(merged) == 0 {
		return nil, errors.New("bedrock: messages are required")
	}
	modelID := c.resolveModelID(req)
	if modelID == "" {
		return nil, errors.New("bedrock: model identifier is required")
	}
	// Enforce provider constraints early when thinking is enabled.
	if req.Thinking != nil && req.Thinking.Enable {
		if err := transcript.ValidateBedrock(merged, true); err != nil {
			return nil, fmt.Errorf("bedrock: invalid message ordering with thinking enabled (run=%s, model=%s): %w", req.RunID, modelID, err)
		}
	}
	messages, system, err := encodeMessages(ctx, merged)
	if err != nil {
		return nil, err
	}
	toolConfig := encodeTools(ctx, req.Tools)
	return &requestParts{
		modelID:    modelID,
		messages:   messages,
		system:     system,
		toolConfig: toolConfig,
	}, nil
}

// resolveModelID decides which concrete model ID to use based on Request.Model
// and Request.ModelClass. Request.Model takes precedence; when empty, the class
// is mapped to the configured identifiers. Falls back to the default model.
func (c *Client) resolveModelID(req model.Request) string {
	if s := req.Model; s != "" {
		return s
	}
	switch string(req.ModelClass) {
	case string(model.ModelClassHighReasoning):
		if c.highModel != "" {
			return c.highModel
		}
	case string(model.ModelClassSmall):
		if c.smallModel != "" {
			return c.smallModel
		}
	}
	return c.defaultModel
}

func (c *Client) buildConverseInput(parts *requestParts, req model.Request) *bedrockruntime.ConverseInput {
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(parts.modelID),
		Messages: parts.messages,
	}
	// Encode response_format for unary calls as well.
	if rf := req.ResponseFormat; rf != nil {
		fields := map[string]any{}
		if len(rf.JSONSchema) > 0 {
			name := rf.SchemaName
			if name == "" {
				name = "result"
			}
			var schema any
			if err := json.Unmarshal(rf.JSONSchema, &schema); err == nil {
				fields["response_format"] = map[string]any{
					"type": "json_schema",
					"json_schema": map[string]any{
						"name":   name,
						"schema": schema,
					},
				}
			}
		} else if rf.JSONOnly {
			fields["response_format"] = map[string]any{"type": "json_object"}
		}
		if len(fields) > 0 {
			input.AdditionalModelRequestFields = document.NewLazyDocument(&fields)
		}
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

func (c *Client) buildConverseStreamInput(parts *requestParts, req model.Request, thinking thinkingConfig) *bedrockruntime.ConverseStreamInput {
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(parts.modelID),
		Messages: parts.messages,
	}
	// Encode response_format when requested (JSON-only or schema-constrained).
	if rf := req.ResponseFormat; rf != nil {
		fields := map[string]any{}
		if len(rf.JSONSchema) > 0 {
			name := rf.SchemaName
			if name == "" {
				name = "result"
			}
			var schema any
			if err := json.Unmarshal(rf.JSONSchema, &schema); err == nil {
				fields["response_format"] = map[string]any{
					"type": "json_schema",
					"json_schema": map[string]any{
						"name":   name,
						"schema": schema,
					},
				}
			}
		} else if rf.JSONOnly {
			fields["response_format"] = map[string]any{
				"type": "json_object",
			}
		}
		if len(fields) > 0 {
			input.AdditionalModelRequestFields = document.NewLazyDocument(&fields)
		}
	}
	if len(parts.system) > 0 {
		input.System = parts.system
	}
	if parts.toolConfig != nil {
		input.ToolConfig = parts.toolConfig
	}
	if thinking.enable {
		fields := map[string]any{
			"thinking": map[string]any{
				"type":          "enabled",
				"budget_tokens": thinking.budget,
			},
		}
		if thinking.interleaved {
			fields["anthropic_beta"] = []string{"interleaved-thinking-2025-05-14"}
		}
		input.AdditionalModelRequestFields = document.NewLazyDocument(&fields)
	}
	if cfg := c.inferenceConfig(req.MaxTokens, req.Temperature); cfg != nil {
		input.InferenceConfig = cfg
	}
	return input
}

func (c *Client) resolveThinking(req model.Request, parts *requestParts) thinkingConfig {
	// Disable thinking when requesting schema/JSON-only responses.
	if req.ResponseFormat != nil {
		return thinkingConfig{}
	}
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
	return thinkingConfig{
		enable:      true,
		interleaved: req.Thinking.Interleaved,
		budget:      budget,
	}
}

// validateThinkingPreconditions enforces provider contract: when thinking is
// enabled, the most recent assistant message in the request must start with a
// reasoningContent block, preceding tool_use/tool_result blocks. We fail fast
// if this precondition is not satisfied, rather than relying on Bedrock to
// reject the request.
// Note: Bedrock interleaved-thinking requires reasoning to precede tool_use within the
// same assistant message. We rely on upstream callers to provide structured thinking parts
// (captured from prior turns) in Messages when tools are used so the encoder can place them
// before tool_use content. We do not disable thinking automatically here.

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

func encodeMessages(ctx context.Context, msgs []*model.Message) ([]brtypes.Message, []brtypes.SystemContentBlock, error) {
	conversation := make([]brtypes.Message, 0, len(msgs))
	system := make([]brtypes.SystemContentBlock, 0, len(msgs))
	for _, m := range msgs {
		// Build content blocks from parts
		if m.Role == "system" {
			for _, p := range m.Parts {
				if tp, ok := p.(model.TextPart); ok && tp.Text != "" {
					system = append(system, &brtypes.SystemContentBlockMemberText{Value: tp.Text})
				}
			}
			continue
		}
		blocks := make([]brtypes.ContentBlock, 0, 1+len(m.Parts))
		for _, part := range m.Parts {
			switch v := part.(type) {
			case model.ThinkingPart:
				// Encode only provider-valid variants.
				if v.Signature != "" && v.Text != "" {
					blocks = append(blocks, &brtypes.ContentBlockMemberReasoningContent{
						Value: &brtypes.ReasoningContentBlockMemberReasoningText{
							Value: brtypes.ReasoningTextBlock{
								Text:      aws.String(v.Text),
								Signature: aws.String(v.Signature),
							},
						},
					})
					break
				}
				if len(v.Redacted) > 0 {
					blocks = append(blocks, &brtypes.ContentBlockMemberReasoningContent{
						Value: &brtypes.ReasoningContentBlockMemberRedactedContent{
							Value: v.Redacted,
						},
					})
					break
				}
			case model.TextPart:
				if v.Text != "" {
					blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: v.Text})
				}
			case model.ToolUsePart:
				// Encode assistant-declared tool_use with optional ID and JSON input.
				tb := brtypes.ToolUseBlock{}
				if v.Name != "" {
					name := v.Name
					tb.Name = aws.String(name)
				}
				if v.ID != "" {
					id := v.ID
					tb.ToolUseId = aws.String(id)
				}
				tb.Input = toDocument(ctx, v.Input)
				blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{Value: tb})
			case model.ToolResultPart:
				// Bedrock expects tool_result blocks in user messages, correlated to a prior tool_use.
				// Encode content as text when Content is a string; otherwise as a JSON document.
				tr := brtypes.ToolResultBlock{
					ToolUseId: aws.String(v.ToolUseID),
				}
				if s, ok := v.Content.(string); ok {
					tr.Content = []brtypes.ToolResultContentBlock{
						&brtypes.ToolResultContentBlockMemberText{Value: s},
					}
				} else {
					doc := toDocument(ctx, v.Content)
					tr.Content = []brtypes.ToolResultContentBlock{
						&brtypes.ToolResultContentBlockMemberJson{Value: doc},
					}
				}
				blocks = append(blocks, &brtypes.ContentBlockMemberToolResult{Value: tr})
			}
		}
		if len(blocks) == 0 {
			continue
		}
		var brrole brtypes.ConversationRole
		if m.Role == "user" {
			brrole = brtypes.ConversationRoleUser
		} else {
			brrole = brtypes.ConversationRoleAssistant
		}
		conversation = append(conversation, brtypes.Message{
			Role:    brrole,
			Content: blocks,
		})
	}
	if len(conversation) == 0 {
		return nil, nil, errors.New("bedrock: at least one user/assistant message is required")
	}
	return conversation, system, nil
}

func encodeTools(ctx context.Context, defs []*model.ToolDefinition) *brtypes.ToolConfiguration {
	if len(defs) == 0 {
		return nil
	}
	tools := make([]brtypes.Tool, 0, len(defs))
	for _, def := range defs {
		if def == nil {
			continue
		}
		name := def.Name
		if name == "" {
			continue
		}
		schemaDoc := toDocument(ctx, def.InputSchema)
		spec := brtypes.ToolSpecification{
			Name:        aws.String(name),
			Description: aws.String(def.Description),
			InputSchema: &brtypes.ToolInputSchemaMemberJson{Value: schemaDoc},
		}
		tools = append(tools, &brtypes.ToolMemberToolSpec{Value: spec})
	}
	if len(tools) == 0 {
		return nil
	}
	return &brtypes.ToolConfiguration{Tools: tools}
}

func toDocument(ctx context.Context, schema any) document.Interface {
	if schema == nil {
		m := map[string]any{"type": "object"}
		return lazyDocument(m)
	}
	switch v := schema.(type) {
	case document.Interface:
		return v
	case json.RawMessage:
		var decoded any
		if len(v) == 0 {
			return lazyDocument(map[string]any{"type": "object"})
		}
		if err := json.Unmarshal(v, &decoded); err != nil {
			log.Error(ctx, err, log.KV{K: "component", V: "inference-engine"},
				log.KV{K: "event", V: "failed to unmarshal schema"})
			return lazyDocument(map[string]any{"type": "object"})
		}
		return lazyDocument(decoded)
	default:
		return lazyDocument(v)
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
				if v.Value == "" {
					continue
				}
				resp.Content = append(resp.Content, model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: v.Value}}})
			case *brtypes.ContentBlockMemberToolUse:
				payload := decodeDocument(v.Value.Input)
				name := ""
				if v.Value.Name != nil {
					name = *v.Value.Name
				}
				var id string
				if v.Value.ToolUseId != nil {
					id = *v.Value.ToolUseId
				}
				resp.ToolCalls = append(resp.ToolCalls, model.ToolCall{Name: tools.Ident(name), Payload: payload, ID: id})
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
	return document.NewLazyDocument(&v)
}
