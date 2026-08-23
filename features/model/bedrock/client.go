// Package bedrock provides raw and validated adapters backed by the AWS Bedrock
// Converse API. It mirrors the inference-engine request pipeline used
// in production systems: split system vs. conversational messages, encode tool
// schemas into Bedrock's ToolConfiguration, and translate Converse responses
// (text + tool_use blocks) back into planner-friendly structures.
package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/big"
	"slices"
	"strings"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"goa.design/goa-ai/features/model/internal/claudebeta"
	"goa.design/goa-ai/features/model/internal/claudecaps"
	"goa.design/goa-ai/features/model/internal/tooluseid"
	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const (
	bedrockProviderName          = "bedrock"
	minBedrockCitationCoordinate = -1 << 31
	maxBedrockCitationCoordinate = 1<<31 - 1
)

// RuntimeClient mirrors the subset of the AWS Bedrock runtime client required
// by the adapter. It matches *bedrockruntime.Client so callers can pass either
// the real client or a mock in tests.
type RuntimeClient interface {
	Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error)
	CountTokens(ctx context.Context, params *bedrockruntime.CountTokensInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.CountTokensOutput, error)
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
	// MaxTokens. When zero or negative, the client omits MaxTokens so Bedrock
	// uses its own default.
	MaxTokens int

	// Temperature is used when a request does not specify Temperature.
	Temperature float32

	// Logger is used for non-fatal diagnostics inside the Bedrock adapter.
	// When nil, defaults to a no-op logger.
	Logger telemetry.Logger
}

// provider translates canonical requests to AWS Bedrock Converse.
type provider struct {
	runtime      RuntimeClient
	defaultModel string
	highModel    string
	smallModel   string
	maxTok       int
	temp         float32
	logger       telemetry.Logger
}

type requestParts struct {
	modelID                 string
	modelClass              model.ModelClass
	messages                []brtypes.Message
	system                  []brtypes.SystemContentBlock
	outputConfig            *brtypes.OutputConfig
	toolConfig              *brtypes.ToolConfiguration
	additionalModelFields   map[string]any
	toolNameCanonicalToProv map[string]string
	toolNameProvToCanonical map[string]string

	// structuredOutputToolName is non-empty exactly when prepareRequest chose
	// to express Request.StructuredOutput as the forced tool call described in
	// structuredOutputUsesToolFallback. Complete and Stream read this single
	// decision instead of re-deriving it from modelID.
	structuredOutputToolName string
}

type thinkingConfig struct {
	enable      bool
	adaptive    bool // Opus 4.6+: use type:"adaptive" (no budget, interleaved is automatic)
	interleaved bool
	budget      int
}

var (
	_ model.Provider     = (*provider)(nil)
	_ model.TokenCounter = (*provider)(nil)
)

// New initializes a validated Bedrock-powered model client.
func New(aws *bedrockruntime.Client, opts Options) (model.Client, error) {
	raw, err := NewProvider(aws, opts)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// NewProvider initializes the raw Bedrock provider used beneath
// model.NewClient. Callers use it to install provider-side middleware before
// final validation.
func NewProvider(aws *bedrockruntime.Client, opts Options) (model.Provider, error) {
	opts.Runtime = aws
	if opts.Runtime == nil {
		return nil, errors.New("bedrock runtime client is required")
	}
	// DefaultModel should be provided; High/Small are optional but recommended.
	if opts.DefaultModel == "" {
		return nil, errors.New("default model identifier is required")
	}
	if _, err := bedrockInt32("default max tokens", opts.MaxTokens); err != nil {
		return nil, err
	}
	if err := validateBedrockTemperature(opts.Temperature); err != nil {
		return nil, err
	}
	maxTokens := opts.MaxTokens
	logger := opts.Logger
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}
	c := &provider{
		runtime:      opts.Runtime,
		defaultModel: opts.DefaultModel,
		highModel:    opts.HighModel,
		smallModel:   opts.SmallModel,
		maxTok:       maxTokens,
		temp:         opts.Temperature,
		logger:       logger,
	}
	return c, nil
}

// Complete issues a chat completion request to the configured Bedrock model
// using the Converse API and translates the response into planner-friendly
// structures (assistant messages + tool calls).
func (c *provider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	parts, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	input, err := c.buildConverseInput(parts, req)
	if err != nil {
		return nil, err
	}
	output, err := c.runtime.Converse(ctx, input)
	if err != nil {
		if isRateLimited(err) {
			return nil, fmt.Errorf("%w: %w", model.ErrRateLimited, err)
		}
		return nil, wrapBedrockError("converse", err)
	}
	resp, err := translateResponse(output, parts.toolNameProvToCanonical, parts.modelID, parts.modelClass)
	if err != nil {
		usage := translateBedrockUsage(output, parts.modelID, parts.modelClass)
		return nil, contract.RejectProviderOutput(&usage, err)
	}
	if parts.structuredOutputToolName != "" {
		if err := reifyStructuredOutputToolFallback(resp, parts.structuredOutputToolName); err != nil {
			return nil, contract.RejectResponse(resp, err)
		}
	}
	return resp, nil
}

// CountTokens asks Bedrock to count the exact input tokens for req using the
// same Converse request preparation path as Complete, except that replayed
// thinking blocks are omitted per the model.TokenCounter contract. Bedrock
// rejects thinking signatures issued by any other model ("Invalid signature
// in thinking block"), and the Claude 5 generation does not support
// CountTokens at all, so the count input must never carry thinking content.
func (c *provider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	countReq := model.CountingRequest(req)
	parts, err := c.prepareRequest(countReq)
	if err != nil {
		return model.TokenCount{}, err
	}
	// Runtime CountTokens is a foundation-model operation: translate the resolved
	// model ID (which may be a cross-region inference profile) to its foundation
	// model ID for the wire request. The returned TokenCount keeps parts.modelID
	// so callers still observe the configured profile/class.
	foundationModelID, err := FoundationModelID(parts.modelID)
	if err != nil {
		return model.TokenCount{}, err
	}
	output, err := c.runtime.CountTokens(ctx, c.buildCountTokensInput(parts, countReq, foundationModelID))
	var inputTokens int
	if err != nil {
		var ok bool
		inputTokens, ok = promptTooLongTokenCount(err)
		if !ok {
			return model.TokenCount{}, wrapBedrockError("count_tokens", err)
		}
	} else {
		if output.InputTokens == nil {
			return model.TokenCount{}, errors.New("bedrock: count_tokens response missing input tokens")
		}
		inputTokens = int(*output.InputTokens)
	}
	return model.TokenCount{
		Model:       parts.modelID,
		ModelClass:  parts.modelClass,
		InputTokens: inputTokens,
		Exact:       true,
	}, nil
}

// Stream invokes the Bedrock ConverseStream API and adapts incremental events
// into model.Chunks so planners can surface partial responses. Structured
// output streams emit completion_delta previews plus one canonical completion
// payload before stop.
func (c *provider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	parts, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	thinking := c.resolveThinking(req, parts)
	input, err := c.buildConverseStreamInput(parts, req, thinking)
	if err != nil {
		return nil, err
	}
	out, err := c.runtime.ConverseStream(ctx, input, c.streamOptions(thinking)...)
	if err != nil {
		var closeErr error
		if out != nil {
			if stream := out.GetStream(); stream != nil {
				closeErr = stream.Close()
			}
		}
		if isRateLimited(err) {
			return nil, errors.Join(fmt.Errorf("%w: %w", model.ErrRateLimited, err), closeErr)
		}
		return nil, errors.Join(wrapBedrockError("converse_stream", err), closeErr)
	}
	stream := out.GetStream()
	if stream == nil {
		return nil, errors.New("bedrock: stream output missing event stream")
	}
	streamer := newBedrockStreamer(
		ctx,
		stream,
		parts.toolNameProvToCanonical,
		parts.modelID,
		parts.modelClass,
		req.StructuredOutput,
		parts.structuredOutputToolName,
		contract,
	)
	return streamer, nil
}

func (c *provider) prepareRequest(req *model.Request) (*requestParts, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("bedrock: messages are required")
	}
	modelID := c.resolveModelID(req)
	if modelID == "" {
		return nil, errors.New("bedrock: model identifier is required")
	}
	// Enforce provider constraints early when thinking is enabled.
	// Adaptive thinking (Opus 4.6+) lets the model skip thinking blocks
	// entirely, so the thinking-first ordering rule does not apply.
	thinkingEnabled := req.Thinking != nil && req.Thinking.Enable
	if thinkingEnabled && !claudecaps.AdaptiveThinkingRequired(modelID) {
		if err := transcript.ValidateBedrock(req.Messages, true); err != nil {
			return nil, fmt.Errorf("bedrock: invalid message ordering with thinking enabled (model=%s): %w", modelID, err)
		}
	}
	// Extract cache options from request.
	var cacheAfterSystem, cacheAfterTools bool
	if req.Cache != nil {
		cacheAfterSystem = req.Cache.AfterSystem
		cacheAfterTools = req.Cache.AfterTools
	}
	// Enforce model-specific cache capabilities: Nova models do not support
	// tool-level cache checkpoints. Fail fast when AfterTools is requested
	// for a Nova model rather than sending an invalid configuration.
	if cacheAfterTools && isNovaModel(modelID) {
		return nil, fmt.Errorf(
			"bedrock: Cache.AfterTools is not supported for Nova models (model=%s)",
			modelID,
		)
	}
	// Build tool configuration and name maps before encoding messages so tool_use
	// names can reuse the exact sanitized identifiers. encodeTools is the single
	// source of truth for name sanitization.
	//
	// Anthropic models reject Bedrock's native structured-output response
	// format (see encodeOutputConfig), so structured output for those models is
	// expressed as a forced single tool call instead, reusing encodeTools for
	// naming, thinking-compatibility, and schema encoding exactly as a real
	// tool call would. This is the one place that decides which wire mechanism
	// to use; Complete and Stream read the decision back from
	// requestParts.structuredOutputToolName instead of re-deriving it.
	toolDefs, toolChoice := req.Tools, req.ToolChoice
	useToolFallback := structuredOutputUsesToolFallback(modelID, req.StructuredOutput)
	if useToolFallback {
		if len(req.Tools) > 0 || req.ToolChoice != nil {
			return nil, errors.New("bedrock: structured output cannot be combined with request tool definitions")
		}
		def, err := structuredOutputToolDefinition(req.StructuredOutput)
		if err != nil {
			return nil, err
		}
		toolDefs = []*model.ToolDefinition{def}
		toolChoice = &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: def.Name}
	}
	toolConfig, additionalModelFields, canonToSan, sanToCanon, err := encodeTools(modelID, toolDefs, toolChoice, cacheAfterTools)
	if err != nil {
		return nil, err
	}
	var outputConfig *brtypes.OutputConfig
	if req.StructuredOutput != nil && !useToolFallback {
		outputConfig, err = encodeOutputConfig(req.StructuredOutput)
		if err != nil {
			return nil, err
		}
	}
	structuredOutputToolName := ""
	if useToolFallback {
		structuredOutputToolName = req.StructuredOutput.Name
	}
	// Bedrock requires toolConfig when messages contain tool_use or tool_result
	// blocks. Fail fast with a clear error rather than letting Bedrock reject
	// the request with a generic validation error.
	if toolConfig == nil && messagesHaveToolBlocks(req.Messages) {
		return nil, fmt.Errorf(
			"bedrock: messages contain tool_use/tool_result but no tools provided in request; " +
				"ensure the planner always passes tools when history has tool blocks",
		)
	}
	if err := registerHistoricalToolNames(req.Messages, canonToSan, sanToCanon); err != nil {
		return nil, err
	}
	messages, system, err := encodeMessages(req.Messages, canonToSan, cacheAfterSystem)
	if err != nil {
		return nil, err
	}
	return &requestParts{
		modelID:                  modelID,
		modelClass:               req.ModelClass,
		messages:                 messages,
		system:                   system,
		outputConfig:             outputConfig,
		toolConfig:               toolConfig,
		additionalModelFields:    additionalModelFields,
		toolNameCanonicalToProv:  canonToSan,
		toolNameProvToCanonical:  sanToCanon,
		structuredOutputToolName: structuredOutputToolName,
	}, nil
}

// resolveModelID decides which concrete model ID to use based on Request.Model
// and Request.ModelClass. Request.Model takes precedence; when empty, the class
// is mapped to the configured identifiers. Falls back to the default model.
func (c *provider) resolveModelID(req *model.Request) string {
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

func (c *provider) buildConverseInput(
	parts *requestParts,
	req *model.Request,
) (*bedrockruntime.ConverseInput, error) {
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(parts.modelID),
		Messages: parts.messages,
	}
	fields := additionalModelFieldsForRequest(parts.additionalModelFields, req)
	if len(parts.system) > 0 {
		input.System = parts.system
	}
	if parts.toolConfig != nil && !usesProviderNativeTools(fields) {
		input.ToolConfig = parts.toolConfig
	}
	if parts.outputConfig != nil {
		input.OutputConfig = parts.outputConfig
	}
	cfg, err := c.inferenceConfig(parts.modelID, req.MaxTokens, req.Temperature)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		input.InferenceConfig = cfg
	}
	if len(fields) > 0 {
		input.AdditionalModelRequestFields = document.NewLazyDocument(&fields)
	}
	return input, nil
}

// buildCountTokensInput builds the Runtime CountTokens request. foundationModelID
// is the foundation model ID resolved from parts.modelID (see FoundationModelID);
// CountTokens rejects the cross-region inference-profile ID that Converse uses.
func (c *provider) buildCountTokensInput(parts *requestParts, req *model.Request, foundationModelID string) *bedrockruntime.CountTokensInput {
	fields := additionalModelFieldsForRequest(parts.additionalModelFields, req)
	converse := brtypes.ConverseTokensRequest{
		Messages: parts.messages,
		System:   parts.system,
	}
	if parts.toolConfig != nil && !usesProviderNativeTools(fields) {
		converse.ToolConfig = parts.toolConfig
	}
	if len(fields) > 0 {
		converse.AdditionalModelRequestFields = document.NewLazyDocument(&fields)
	}
	return &bedrockruntime.CountTokensInput{
		ModelId: aws.String(foundationModelID),
		Input:   &brtypes.CountTokensInputMemberConverse{Value: converse},
	}
}

func (c *provider) buildConverseStreamInput(
	parts *requestParts,
	req *model.Request,
	thinking thinkingConfig,
) (*bedrockruntime.ConverseStreamInput, error) {
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(parts.modelID),
		Messages: parts.messages,
	}
	fields := additionalModelFieldsForRequest(parts.additionalModelFields, req)
	if len(parts.system) > 0 {
		input.System = parts.system
	}
	if parts.toolConfig != nil && !usesProviderNativeTools(fields) {
		input.ToolConfig = parts.toolConfig
	}
	if parts.outputConfig != nil {
		input.OutputConfig = parts.outputConfig
	}
	if thinking.enable {
		if fields == nil {
			fields = map[string]any{}
		}
		if thinking.adaptive {
			// Opus 4.6+: adaptive thinking lets the model decide when and how
			// deeply to reason. Request summarized display explicitly so Bedrock
			// keeps returning visible reasoning text even on models like Opus 4.7
			// where omitted display is now the default. Interleaved thinking is
			// automatic — no beta header required.
			fields["thinking"] = map[string]any{
				"type":    "adaptive",
				"display": "summarized",
			}
		} else {
			thinkingCfg := map[string]any{"type": "enabled"}
			if thinking.budget > 0 {
				thinkingCfg["budget_tokens"] = thinking.budget
			}
			fields["thinking"] = thinkingCfg
			if thinking.interleaved {
				addAnthropicBeta(fields, "interleaved-thinking-2025-05-14")
			}
		}
	}
	if len(fields) > 0 {
		input.AdditionalModelRequestFields = document.NewLazyDocument(&fields)
	}
	cfg, err := c.inferenceConfig(parts.modelID, req.MaxTokens, req.Temperature)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		input.InferenceConfig = cfg
	}
	return input, nil
}

// encodeOutputConfig translates a provider-neutral structured-output request
// into Bedrock's response format configuration. It adapts the canonical
// generated schema to Bedrock's accepted subset without changing the
// completion contract owned by the service.
func encodeOutputConfig(output *model.StructuredOutput) (*brtypes.OutputConfig, error) {
	if output == nil {
		return nil, nil
	}
	if len(output.Schema) == 0 {
		return nil, errors.New("bedrock: structured output requires a schema")
	}
	schema, err := normalizeStructuredOutputSchemaForBedrock(output.Schema)
	if err != nil {
		return nil, err
	}
	def := brtypes.JsonSchemaDefinition{
		Schema: aws.String(string(schema)),
	}
	if output.Name != "" {
		def.Name = aws.String(output.Name)
	}
	return &brtypes.OutputConfig{
		TextFormat: &brtypes.OutputFormat{
			Type:      brtypes.OutputFormatTypeJsonSchema,
			Structure: &brtypes.OutputFormatStructureMemberJsonSchema{Value: def},
		},
	}, nil
}

// structuredOutputUsesToolFallback reports whether a structured-output request
// must be expressed as a forced tool call instead of Bedrock's native
// OutputConfig response format. Bedrock's Converse API translates
// OutputConfig.TextFormat into Anthropic's own (unsupported-on-Converse)
// output_config.format field before forwarding it to Anthropic models, and the
// model then rejects it with "Extra inputs are not permitted" — a documented
// AWS/Anthropic incompatibility, not a request bug. Forcing a single tool call
// is the well-supported alternative that Bedrock validates against the same
// JSON Schema with the same guarantee, so callers see no difference in the
// canonical model.Response.
func structuredOutputUsesToolFallback(modelID string, output *model.StructuredOutput) bool {
	return output != nil && isAnthropicBedrockModel(modelID)
}

// structuredOutputToolDefinition adapts a provider-neutral structured-output
// request into the single forced tool definition used by
// structuredOutputUsesToolFallback. The tool name and description are the
// request's own Name/Description. Its object-shaped input privately wraps the
// declared completion value because Bedrock tools cannot accept an array or
// primitive as their top-level input.
func structuredOutputToolDefinition(output *model.StructuredOutput) (*model.ToolDefinition, error) {
	if len(output.Schema) == 0 {
		return nil, errors.New("bedrock: structured output requires a schema")
	}
	if output.Name == "" {
		return nil, errors.New("bedrock: structured output requires a name")
	}
	input, err := structuredOutputToolInput(output)
	if err != nil {
		return nil, fmt.Errorf("bedrock: structured output %q: %w", output.Name, err)
	}
	return &model.ToolDefinition{
		Name:        output.Name,
		Description: output.Description,
		Input:       input,
	}, nil
}

// reifyStructuredOutputToolFallback rewrites the forced tool_use response
// produced by structuredOutputUsesToolFallback into canonical completion text.
// Provider-issued thinking remains in place; exactly one matching tool call is
// required and every other content part is rejected.
func reifyStructuredOutputToolFallback(resp *model.Response, toolName string) error {
	if len(resp.Content) != 1 {
		return fmt.Errorf("bedrock: structured output tool fallback expected exactly one assistant message, got %d", len(resp.Content))
	}
	parts := resp.Content[0].Parts
	canonical := make([]model.Part, 0, len(parts))
	found := false
	for _, part := range parts {
		switch value := part.(type) {
		case model.ThinkingPart:
			canonical = append(canonical, value)
		case model.ToolUsePart:
			if found {
				return fmt.Errorf("bedrock: structured output tool fallback returned multiple tool calls")
			}
			if value.Name != toolName {
				return fmt.Errorf("bedrock: structured output tool fallback did not return the forced tool call %q", toolName)
			}
			payload, err := unwrapStructuredOutputValue(value.Input)
			if err != nil {
				return fmt.Errorf("bedrock: structured output tool fallback %q: %w", toolName, err)
			}
			canonical = append(canonical, model.TextPart{Text: string(payload)})
			found = true
		default:
			return fmt.Errorf("bedrock: structured output tool fallback returned unexpected content part %T", part)
		}
	}
	if !found {
		return fmt.Errorf("bedrock: structured output tool fallback did not return the forced tool call %q", toolName)
	}
	resp.Content[0].Parts = canonical
	return nil
}

func (c *provider) resolveThinking(req *model.Request, parts *requestParts) thinkingConfig {
	if req.Thinking == nil || !req.Thinking.Enable {
		return thinkingConfig{}
	}
	if forcesToolUse(req.ToolChoice) {
		return thinkingConfig{}
	}
	// Opus 4.6+ requires adaptive thinking: the model dynamically decides when
	// and how deeply to reason. Interleaved thinking is automatic in adaptive
	// mode — no beta header is needed. The legacy type:"enabled" + budget_tokens
	// config is deprecated for Opus 4.6 and produces unreliable signatures.
	if claudecaps.AdaptiveThinkingRequired(parts.modelID) {
		return thinkingConfig{
			enable:   true,
			adaptive: true,
		}
	}
	budget := req.Thinking.BudgetTokens
	return thinkingConfig{
		enable:      true,
		interleaved: req.Thinking.Interleaved,
		budget:      budget,
	}
}

// forcesToolUse reports whether a provider-neutral tool choice requires the
// next assistant turn to contain a tool call. Anthropic-on-Bedrock rejects
// thinking for those requests, regardless of whether the caller names one tool
// or allows any tool. On models with optional thinking the adapter drops the
// thinking config for forced-tool turns; on the always-thinking Claude 5
// generation (Fable/Mythos) forced tool use is unrepresentable and encodeTools
// rejects it.
func forcesToolUse(choice *model.ToolChoice) bool {
	return choice != nil && (choice.Mode == model.ToolChoiceModeAny || choice.Mode == model.ToolChoiceModeTool)
}

// Note on thinking preconditions:
//
// prepareRequest always enforces the shared planner tool-loop invariants.
//
// For legacy models (pre-Opus 4.6) with type:"enabled", Bedrock interleaved
// thinking additionally requires reasoning to precede tool_use within the same
// assistant message. prepareRequest enforces that representability constraint
// via transcript.ValidateBedrock; transcript construction never invents missing
// provider reasoning.
//
// For adaptive thinking models (Opus 4.6+), thinking is optional — the model
// may skip reasoning entirely on simple turns. The thinking-first ordering
// rule does not apply, and the beta header is not needed.

func (c *provider) streamOptions(thinking thinkingConfig) []func(*bedrockruntime.Options) {
	if !thinking.enable || thinking.adaptive {
		// Adaptive thinking (Opus 4.6+) does not require the interleaved
		// thinking beta header — the capability is built into the model.
		return nil
	}
	return []func(*bedrockruntime.Options){
		bedrockruntime.WithAPIOptions(
			smithyhttp.AddHeaderValue("x-amzn-bedrock-beta", "interleaved-thinking-2025-05-14"),
		),
	}
}

func (c *provider) inferenceConfig(
	modelID string,
	maxTokens int,
	temp float32,
) (*brtypes.InferenceConfiguration, error) {
	var cfg brtypes.InferenceConfiguration
	effectiveTemperature := c.effectiveTemperature(temp)
	if err := validateBedrockTemperature(effectiveTemperature); err != nil {
		return nil, err
	}
	tokens := c.effectiveMaxTokens(maxTokens)
	if tokens > 0 {
		converted, err := bedrockInt32("max tokens", tokens)
		if err != nil {
			return nil, err
		}
		cfg.MaxTokens = aws.Int32(converted)
	}
	if claudecaps.TemperatureSupported(modelID) {
		if effectiveTemperature > 0 {
			cfg.Temperature = aws.Float32(effectiveTemperature)
		}
	}
	if cfg.MaxTokens == nil && cfg.Temperature == nil {
		return nil, nil
	}
	return &cfg, nil
}

// bedrockInt32 rejects token limits the AWS SDK cannot represent before
// narrowing.
func bedrockInt32(field string, value int) (int32, error) {
	if value < 0 || int64(value) > math.MaxInt32 {
		return 0, fmt.Errorf("bedrock: %s must be between 0 and %d", field, int64(math.MaxInt32))
	}
	return int32(value), nil
}

// validateBedrockTemperature enforces the Converse API sampling range.
func validateBedrockTemperature(temperature float32) error {
	value := float64(temperature)
	if math.IsNaN(value) || math.IsInf(value, 0) || temperature < 0 || temperature > 1 {
		return errors.New("bedrock: temperature must be between 0 and 1")
	}
	return nil
}

func cloneAdditionalModelFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]any, len(fields))
	maps.Copy(out, fields)
	return out
}

// additionalModelFieldsForRequest selects provider-specific request fields for
// this turn. Bedrock Converse requires ToolConfig whenever the transcript
// already contains toolUse/toolResult blocks. In those resume turns we omit
// Anthropic's native tool-example field so Bedrock sees one tool declaration.
func additionalModelFieldsForRequest(fields map[string]any, req *model.Request) map[string]any {
	out := cloneAdditionalModelFields(fields)
	if len(out) == 0 || !messagesHaveToolBlocks(req.Messages) || !usesProviderNativeTools(out) {
		return out
	}
	delete(out, "tools")
	delete(out, "tool_choice")
	removeAnthropicBeta(out, claudebeta.ToolExamples)
	return out
}

func addAnthropicBeta(fields map[string]any, beta string) {
	if beta == "" {
		return
	}
	values, _ := fields["anthropic_beta"].([]string)
	if slices.Contains(values, beta) {
		return
	}
	fields["anthropic_beta"] = append(values, beta)
}

func removeAnthropicBeta(fields map[string]any, beta string) {
	values, _ := fields["anthropic_beta"].([]string)
	if len(values) == 0 {
		return
	}
	out := values[:0]
	for _, value := range values {
		if value != beta {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		delete(fields, "anthropic_beta")
		return
	}
	fields["anthropic_beta"] = out
}

func (c *provider) effectiveMaxTokens(requested int) int {
	if requested > 0 {
		return requested
	}
	return c.maxTok
}

func (c *provider) effectiveTemperature(requested float32) float32 {
	if requested > 0 {
		return requested
	}
	return c.temp
}

func encodeMessages(msgs []*model.Message, nameMap map[string]string, cacheAfterSystem bool) ([]brtypes.Message, []brtypes.SystemContentBlock, error) {
	toolUseIDs := tooluseid.NewMapper(msgs)

	// docNameMap ensures provider-safe document names are stable and unique
	// within a single request. Bedrock enforces strict filename rules for
	// document blocks; we sanitize user-provided names and suffix duplicates.
	docNameMap := make(map[string]string)
	usedDocNames := make(map[string]struct{})
	nextDocNameID := 0

	conversation := make([]brtypes.Message, 0, len(msgs))
	system := make([]brtypes.SystemContentBlock, 0, len(msgs))
	for _, m := range msgs {
		// Build content blocks from parts
		if m.Role == "system" {
			for _, p := range m.Parts {
				switch v := p.(type) {
				case model.TextPart:
					if v.Text != "" {
						system = append(system, &brtypes.SystemContentBlockMemberText{Value: v.Text})
					}
				case model.CitationsPart:
					return nil, nil, errors.New("bedrock: replaying canonical citations is not supported")
				case model.CacheCheckpointPart:
					system = append(system, &brtypes.SystemContentBlockMemberCachePoint{
						Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault},
					})
				case model.DocumentPart:
					return nil, nil, errors.New("bedrock: document parts are not supported in system messages")
				default:
					return nil, nil, fmt.Errorf("bedrock: unsupported system message part %T", p)
				}
			}
			continue
		}
		blocks := make([]brtypes.ContentBlock, 0, 1+len(m.Parts))
		for _, part := range m.Parts {
			switch v := part.(type) {
			case model.ThinkingPart:
				// Valid variants: signed content (signature present; text may
				// be empty — Opus 4.8-class "omitted" thinking display emits
				// signature-only blocks that must replay verbatim) or
				// redacted bytes. Text without a signature is unreplayable.
				hasSigned := v.Signature != ""
				hasRedacted := len(v.Redacted) > 0
				if hasSigned == hasRedacted || (!hasSigned && v.Text != "") {
					return nil, nil, errors.New("bedrock: thinking part must contain exactly signed content or redacted content")
				}
				if hasSigned {
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
				blocks = append(blocks, &brtypes.ContentBlockMemberReasoningContent{
					Value: &brtypes.ReasoningContentBlockMemberRedactedContent{
						Value: v.Redacted,
					},
				})
			case model.TextPart:
				if v.Text != "" {
					blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: v.Text})
				}
			case model.CitationsPart:
				if m.Role != model.ConversationRoleAssistant {
					return nil, nil, fmt.Errorf(
						"bedrock: citation parts are only supported in assistant messages (role=%s)",
						m.Role,
					)
				}
				block, err := encodeCitationsContent(v)
				if err != nil {
					return nil, nil, err
				}
				blocks = append(blocks, block)
			case model.ImagePart:
				// Bedrock supports image blocks only for user messages (Claude multimodal).
				if m.Role != model.ConversationRoleUser {
					return nil, nil, fmt.Errorf(
						"bedrock: image parts are only supported in user messages (role=%s)",
						m.Role,
					)
				}
				var format brtypes.ImageFormat
				switch v.Format {
				case model.ImageFormatPNG:
					format = brtypes.ImageFormatPng
				case model.ImageFormatJPEG:
					format = brtypes.ImageFormatJpeg
				case model.ImageFormatGIF:
					format = brtypes.ImageFormatGif
				case model.ImageFormatWEBP:
					format = brtypes.ImageFormatWebp
				default:
					return nil, nil, fmt.Errorf("bedrock: unsupported image format %q", v.Format)
				}
				blocks = append(blocks, &brtypes.ContentBlockMemberImage{
					Value: brtypes.ImageBlock{
						Format: format,
						Source: &brtypes.ImageSourceMemberBytes{Value: v.Bytes},
					},
				})
			case model.DocumentPart:
				// Bedrock supports document blocks for user messages.
				if m.Role != model.ConversationRoleUser {
					return nil, nil, fmt.Errorf(
						"bedrock: document parts are only supported in user messages (role=%s)",
						m.Role,
					)
				}
				if v.Name == "" {
					return nil, nil, errors.New("bedrock: document part requires Name")
				}
				var source brtypes.DocumentSource
				isS3Source := false
				switch {
				case len(v.Bytes) > 0:
					source = &brtypes.DocumentSourceMemberBytes{Value: v.Bytes}
				case len(v.Chunks) > 0:
					chunks := make([]brtypes.DocumentContentBlock, 0, len(v.Chunks))
					for i, chunk := range v.Chunks {
						if chunk == "" {
							return nil, nil, fmt.Errorf("bedrock: document part requires non-empty Chunks[%d]", i)
						}
						chunks = append(chunks, &brtypes.DocumentContentBlockMemberText{Value: chunk})
					}
					source = &brtypes.DocumentSourceMemberContent{Value: chunks}
				case v.URI != "":
					isS3Source = true
					if !strings.HasPrefix(v.URI, "s3://") {
						return nil, nil, fmt.Errorf("bedrock: document URI scheme not supported: %q", v.URI)
					}
					s3 := brtypes.S3Location{
						Uri: aws.String(v.URI),
					}
					source = &brtypes.DocumentSourceMemberS3Location{Value: s3}
				case v.Text != "":
					source = &brtypes.DocumentSourceMemberText{Value: v.Text}
				default:
					return nil, nil, errors.New("bedrock: document part requires one of Bytes, Text, Chunks, or URI")
				}
				doc := brtypes.DocumentBlock{
					Name:   aws.String(docNameFor(v.Name, docNameMap, usedDocNames, &nextDocNameID)),
					Source: source,
				}
				if v.Format != "" {
					doc.Format = brtypes.DocumentFormat(v.Format)
				}
				// Bedrock disallows S3Location as a source when citations are enabled.
				if v.Cite && isS3Source {
					return nil, nil, fmt.Errorf("bedrock: document %q cannot enable citations for an S3 source", v.Name)
				}
				if v.Cite {
					doc.Citations = &brtypes.CitationsConfig{Enabled: aws.Bool(true)}
				}
				if v.Context != "" {
					doc.Context = aws.String(v.Context)
				}
				blocks = append(blocks, &brtypes.ContentBlockMemberDocument{Value: doc})
			case model.ToolUsePart:
				// Encode assistant-declared tool_use with its request-scoped provider
				// name, optional ID, and JSON input.
				tb := brtypes.ToolUseBlock{}
				if v.Name != "" {
					providerName, ok := nameMap[v.Name]
					if !ok {
						return nil, nil, fmt.Errorf(
							"bedrock: historical canonical tool name %q is not registered",
							v.Name,
						)
					}
					tb.Name = aws.String(providerName)
				}
				if v.ID != "" {
					if id := toolUseIDs.ID(v.ID); id != "" {
						tb.ToolUseId = aws.String(id)
					}
				}
				if tb.Input == nil {
					var err error
					tb.Input, err = toDocument(v.Input)
					if err != nil {
						return nil, nil, fmt.Errorf("bedrock: encode tool_use %q input: %w", v.Name, err)
					}
				}
				blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{Value: tb})
			case model.ToolResultPart:
				// Bedrock expects tool_result blocks in user messages, correlated to a prior tool_use.
				// Encode content as text when Content is a string; otherwise as a JSON document.
				tr := brtypes.ToolResultBlock{}
				if id := toolUseIDs.ID(v.ToolUseID); id != "" {
					tr.ToolUseId = aws.String(id)
				}
				if s, ok := v.Content.(string); ok {
					tr.Content = []brtypes.ToolResultContentBlock{
						&brtypes.ToolResultContentBlockMemberText{Value: s},
					}
				} else {
					doc, err := toDocument(v.Content)
					if err != nil {
						return nil, nil, fmt.Errorf("bedrock: encode tool_result %q content: %w", v.ToolUseID, err)
					}
					tr.Content = []brtypes.ToolResultContentBlock{
						&brtypes.ToolResultContentBlockMemberJson{Value: doc},
					}
				}
				blocks = append(blocks, &brtypes.ContentBlockMemberToolResult{Value: tr})
			case model.CacheCheckpointPart:
				blocks = append(blocks, &brtypes.ContentBlockMemberCachePoint{
					Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault},
				})
			default:
				return nil, nil, fmt.Errorf("bedrock: unsupported %s message part %T", m.Role, part)
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
	// Policy-driven: append a cache checkpoint after system messages when requested.
	if cacheAfterSystem && len(system) > 0 {
		system = append(system, &brtypes.SystemContentBlockMemberCachePoint{
			Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault},
		})
	}
	return conversation, system, nil
}

// encodeCitationsContent reconstructs the Bedrock citation block represented by
// one canonical assistant part. Canonical validation owns content and source
// invariants; this adapter additionally requires Bedrock's document location.
func encodeCitationsContent(part model.CitationsPart) (brtypes.ContentBlock, error) {
	if err := model.ValidateCitationsPart(part); err != nil {
		return nil, fmt.Errorf("bedrock: invalid citations part: %w", err)
	}
	citations := make([]brtypes.Citation, 0, len(part.Citations))
	for index, citation := range part.Citations {
		encoded, err := encodeCitation(citation)
		if err != nil {
			return nil, fmt.Errorf("bedrock: citation %d: %w", index, err)
		}
		citations = append(citations, encoded)
	}
	return &brtypes.ContentBlockMemberCitationsContent{
		Value: brtypes.CitationsContentBlock{
			Content: []brtypes.CitationGeneratedContent{
				&brtypes.CitationGeneratedContentMemberText{Value: part.Text},
			},
			Citations: citations,
		},
	}, nil
}

// encodeCitation reconstructs one Bedrock citation without dropping optional
// source identity or source excerpts.
func encodeCitation(citation model.Citation) (brtypes.Citation, error) {
	location, err := encodeCitationLocation(citation.Location)
	if err != nil {
		return brtypes.Citation{}, err
	}
	out := brtypes.Citation{
		Location: location,
	}
	if citation.Title != "" {
		out.Title = aws.String(citation.Title)
	}
	if citation.Source != "" {
		out.Source = aws.String(citation.Source)
	}
	if len(citation.SourceContent) > 0 {
		out.SourceContent = make([]brtypes.CitationSourceContent, 0, len(citation.SourceContent))
		for _, content := range citation.SourceContent {
			out.SourceContent = append(out.SourceContent, &brtypes.CitationSourceContentMemberText{Value: content})
		}
	}
	return out, nil
}

// encodeCitationLocation converts the one canonical document location into the
// corresponding Bedrock union member.
func encodeCitationLocation(location model.CitationLocation) (brtypes.CitationLocation, error) {
	switch {
	case location.DocumentChar != nil:
		value := location.DocumentChar
		documentIndex, start, end, err := encodeCitationCoordinates(value.DocumentIndex, value.Start, value.End)
		if err != nil {
			return nil, err
		}
		return &brtypes.CitationLocationMemberDocumentChar{
			Value: brtypes.DocumentCharLocation{
				DocumentIndex: documentIndex,
				Start:         start,
				End:           end,
			},
		}, nil
	case location.DocumentChunk != nil:
		value := location.DocumentChunk
		documentIndex, start, end, err := encodeCitationCoordinates(value.DocumentIndex, value.Start, value.End)
		if err != nil {
			return nil, err
		}
		return &brtypes.CitationLocationMemberDocumentChunk{
			Value: brtypes.DocumentChunkLocation{
				DocumentIndex: documentIndex,
				Start:         start,
				End:           end,
			},
		}, nil
	case location.DocumentPage != nil:
		value := location.DocumentPage
		documentIndex, start, end, err := encodeCitationCoordinates(value.DocumentIndex, value.Start, value.End)
		if err != nil {
			return nil, err
		}
		return &brtypes.CitationLocationMemberDocumentPage{
			Value: brtypes.DocumentPageLocation{
				DocumentIndex: documentIndex,
				Start:         start,
				End:           end,
			},
		}, nil
	default:
		return nil, errors.New("requires exactly one Bedrock document location")
	}
}

// encodeCitationCoordinates narrows canonical int coordinates to Bedrock's
// int32 wire fields without allowing a lossy replay.
func encodeCitationCoordinates(documentIndex, start, end int) (*int32, *int32, *int32, error) {
	if documentIndex < minBedrockCitationCoordinate || documentIndex > maxBedrockCitationCoordinate {
		return nil, nil, nil, fmt.Errorf("document index %d is outside Bedrock's int32 range", documentIndex)
	}
	if start < minBedrockCitationCoordinate || start > maxBedrockCitationCoordinate {
		return nil, nil, nil, fmt.Errorf("start %d is outside Bedrock's int32 range", start)
	}
	if end < minBedrockCitationCoordinate || end > maxBedrockCitationCoordinate {
		return nil, nil, nil, fmt.Errorf("end %d is outside Bedrock's int32 range", end)
	}
	return aws.Int32(int32(documentIndex)), aws.Int32(int32(start)), aws.Int32(int32(end)), nil
}

func encodeTools(
	modelID string,
	defs []*model.ToolDefinition,
	choice *model.ToolChoice,
	cacheAfterTools bool,
) (*brtypes.ToolConfiguration, map[string]any, map[string]string, map[string]string, error) {
	if len(defs) == 0 {
		if choice == nil || choice.Mode == model.ToolChoiceModeNone {
			return nil, nil, nil, nil, nil
		}
		return nil, nil, nil, nil, fmt.Errorf("bedrock: tool choice is set but no tools are defined")
	}
	canonToSan, sanToCanon, err := toolname.BuildMaps(defs)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("bedrock: %w", err)
	}
	toolList := make([]brtypes.Tool, 0, len(defs))
	anthropicModel := isAnthropicBedrockModel(modelID)
	anthropicTools := make([]map[string]any, 0, len(defs))
	anthropicHasExamples := false
	for _, def := range defs {
		canonical := def.Name
		sanitized := canonToSan[canonical]
		if def.Description == "" {
			return nil, nil, nil, nil, fmt.Errorf("bedrock: tool %q is missing description", canonical)
		}
		input := def.Input.Contract()
		inputSchema := input.Schema
		hasExample := input.ExampleJSON != nil
		if anthropicModel && hasExample {
			if input.SchemaWithoutRootExample == nil {
				return nil, nil, nil, nil, fmt.Errorf("bedrock: tool %q example JSON requires schema without root example", canonical)
			}
			inputSchema = input.SchemaWithoutRootExample
			anthropicHasExamples = true
		}
		if anthropicModel {
			anthropicTool, err := anthropicToolDefinition(sanitized, def, hasExample)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			anthropicTools = append(anthropicTools, anthropicTool)
		}
		schemaDoc, err := schemaDocument(inputSchema)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("bedrock: tool %q schema: %w", canonical, err)
		}
		spec := brtypes.ToolSpecification{
			Name:        aws.String(sanitized),
			Description: aws.String(def.Description),
			InputSchema: &brtypes.ToolInputSchemaMemberJson{Value: schemaDoc},
		}
		toolList = append(toolList, &brtypes.ToolMemberToolSpec{Value: spec})
	}
	// Policy-driven: append a cache checkpoint after tools when requested.
	// Note: Only Claude models support tool-level cache checkpoints; Nova does not.
	if cacheAfterTools {
		toolList = append(toolList, &brtypes.ToolMemberCachePoint{
			Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault},
		})
	}

	if choice == nil {
		return &brtypes.ToolConfiguration{Tools: toolList}, anthropicToolExampleFields(anthropicTools, anthropicHasExamples, nil), canonToSan, sanToCanon, nil
	}

	// Claude 5 generation models (Fable/Mythos) run with always-on adaptive
	// thinking, and Anthropic rejects forced tool use (tool_choice any/tool)
	// whenever thinking is active. Earlier models let the adapter drop the
	// thinking config for forced-tool turns; Fable has no non-thinking mode, so
	// the request is unrepresentable. Fail fast with a precise error instead of
	// letting Bedrock return an opaque ValidationException: callers must use
	// auto and steer tool selection through prompting, enforcing the
	// must-call-tool contract on the response.
	if forcesToolUse(choice) && claudecaps.IsFableGeneration(modelID) {
		return nil, nil, nil, nil, fmt.Errorf(
			"bedrock: model %q does not support forced tool choice (tool_choice mode %q): thinking is always on and incompatible with forced tool use; use mode \"auto\" and steer tool selection through prompting",
			modelID, choice.Mode,
		)
	}

	cfg := brtypes.ToolConfiguration{
		Tools: toolList,
	}

	switch choice.Mode {
	case "", model.ToolChoiceModeAuto:
		// Auto is the provider default; omit ToolChoice to preserve existing
		// behavior.
	case model.ToolChoiceModeNone:
		return nil, nil, nil, nil, errors.New(
			"bedrock: tool choice mode \"none\" is unsupported when tools are defined",
		)
	case model.ToolChoiceModeAny:
		cfg.ToolChoice = &brtypes.ToolChoiceMemberAny{
			Value: brtypes.AnyToolChoice{},
		}
	case model.ToolChoiceModeTool:
		if choice.Name == "" {
			return nil, nil, nil, nil, fmt.Errorf("bedrock: tool choice mode %q requires a tool name", choice.Mode)
		}
		if !hasToolDefinition(defs, choice.Name) {
			return nil, nil, nil, nil, fmt.Errorf("bedrock: tool choice name %q does not match any tool", choice.Name)
		}
		sanitized := toolname.Sanitize(choice.Name)
		if canonical, ok := sanToCanon[sanitized]; !ok || canonical != choice.Name {
			return nil, nil, nil, nil, fmt.Errorf("bedrock: tool choice name %q does not match any tool", choice.Name)
		}
		cfg.ToolChoice = &brtypes.ToolChoiceMemberTool{
			Value: brtypes.SpecificToolChoice{Name: aws.String(sanitized)},
		}
	default:
		return nil, nil, nil, nil, fmt.Errorf("bedrock: unsupported tool choice mode %q", choice.Mode)
	}

	return &cfg, anthropicToolExampleFields(anthropicTools, anthropicHasExamples, choice), canonToSan, sanToCanon, nil
}

// registerHistoricalToolNames extends the request-scoped provider mapping with
// canonical tool identifiers already present in transcript history. Historical
// tools remain representable even when they are not advertised in this turn.
func registerHistoricalToolNames(
	messages []*model.Message,
	canonToSan, sanToCanon map[string]string,
) error {
	for _, message := range messages {
		for _, part := range message.Parts {
			use, ok := part.(model.ToolUsePart)
			if !ok || use.Name == "" {
				continue
			}
			if _, err := registerToolName(use.Name, canonToSan, sanToCanon); err != nil {
				return err
			}
		}
	}
	return nil
}

// registerToolName adds one canonical identifier to the request-scoped Bedrock
// name mapping and rejects collisions before any provider request is sent.
func registerToolName(
	canonical string,
	canonToSan, sanToCanon map[string]string,
) (string, error) {
	if provider, ok := canonToSan[canonical]; ok {
		return provider, nil
	}
	sanitized := toolname.Sanitize(canonical)
	if prev, ok := sanToCanon[sanitized]; ok && prev != canonical {
		return "", fmt.Errorf(
			"bedrock: tool name %q sanitizes to %q which collides with %q",
			canonical,
			sanitized,
			prev,
		)
	}
	sanToCanon[sanitized] = canonical
	canonToSan[canonical] = sanitized
	return sanitized, nil
}

func anthropicToolDefinition(name string, def *model.ToolDefinition, includeExample bool) (map[string]any, error) {
	input := def.Input.Contract()
	inputSchema := input.Schema
	if includeExample {
		inputSchema = input.SchemaWithoutRootExample
	}
	schema, err := schemaMap(inputSchema)
	if err != nil {
		return nil, fmt.Errorf("bedrock: tool %q Anthropic schema: %w", def.Name, err)
	}
	tool := map[string]any{
		"name":         name,
		"description":  def.Description,
		"input_schema": schema,
	}
	if includeExample {
		example, err := schemaMap(input.ExampleJSON)
		if err != nil {
			return nil, fmt.Errorf("bedrock: tool %q Anthropic example: %w", def.Name, err)
		}
		tool["input_examples"] = []map[string]any{example}
	}
	return tool, nil
}

func anthropicToolExampleFields(tools []map[string]any, hasExamples bool, choice *model.ToolChoice) map[string]any {
	if !hasExamples {
		return nil
	}
	fields := map[string]any{
		"tools": tools,
	}
	if choice != nil {
		switch choice.Mode {
		case "", model.ToolChoiceModeAuto, model.ToolChoiceModeNone:
		case model.ToolChoiceModeAny:
			fields["tool_choice"] = map[string]any{"type": "any"}
		case model.ToolChoiceModeTool:
			fields["tool_choice"] = map[string]any{
				"type": "tool",
				"name": toolname.Sanitize(choice.Name),
			}
		}
	}
	addAnthropicBeta(fields, claudebeta.ToolExamples)
	return fields
}

// usesProviderNativeTools reports whether additionalModelFields carries the
// provider's tool declaration. Anthropic tool examples require the native
// `tools` field, which must be the only provider-visible tool list in the
// request or Claude rejects the duplicate names before planning starts.
func usesProviderNativeTools(fields map[string]any) bool {
	if len(fields) == 0 {
		return false
	}
	_, ok := fields["tools"]
	return ok
}

// sanitizeDocumentName maps an arbitrary user-provided document name (typically a
// filename) to a value that conforms to Bedrock's document name constraints:
// - allowed characters: alphanumerics, whitespace, hyphens, parentheses, square brackets
// - no more than one consecutive whitespace character
//
// The result is trimmed and may be empty if the input contains no usable runes.
func sanitizeDocumentName(in string) string {
	if in == "" {
		return ""
	}
	var out []rune
	out = make([]rune, 0, len(in))
	prevSpace := false
	for _, r := range in {
		// Allowed:
		// - letters/digits
		// - whitespace (collapsed)
		// - '-', '(', ')', '[', ']'
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out = append(out, r)
			prevSpace = false
		case r == '-' || r == '(' || r == ')' || r == '[' || r == ']':
			out = append(out, r)
			prevSpace = false
		case unicode.IsSpace(r):
			if prevSpace {
				continue
			}
			out = append(out, ' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(string(out))
}

func docNameFor(original string, docNameMap map[string]string, usedDocNames map[string]struct{}, nextDocNameID *int) string {
	if original == "" {
		return "document"
	}
	if v, ok := docNameMap[original]; ok {
		return v
	}
	base := sanitizeDocumentName(original)
	if base == "" {
		base = "document"
	}
	name := base
	if _, ok := usedDocNames[name]; ok {
		for {
			*nextDocNameID++
			candidate := fmt.Sprintf("%s (%d)", base, *nextDocNameID)
			if _, exists := usedDocNames[candidate]; !exists {
				name = candidate
				break
			}
		}
	}
	usedDocNames[name] = struct{}{}
	docNameMap[original] = name
	return name
}

// toDocument translates a canonical JSON-bearing value into Bedrock's Smithy
// document without converting JSON numbers through float64.
func toDocument(value any) (document.Interface, error) {
	if value == nil {
		return nil, errors.New("document value is required")
	}
	switch v := value.(type) {
	case rawjson.Message:
		return decodeJSONDocument(v)
	case json.RawMessage:
		return decodeJSONDocument(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return decodeJSONDocument(data)
	}
}

// decodeJSONDocument preserves the canonical JSON number spelling while
// materializing the object form required by the Bedrock SDK.
func decodeJSONDocument(data []byte) (document.Interface, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("document JSON is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("document contains multiple JSON values")
		}
		return nil, err
	}
	decoded, err := smithyDocumentValue(decoded)
	if err != nil {
		return nil, err
	}
	return lazyDocument(decoded), nil
}

func schemaDocument(schema rawjson.Message) (document.Interface, error) {
	m, err := schemaMap(schema)
	if err != nil {
		return nil, err
	}
	return lazyDocument(m), nil
}

func schemaMap(schema rawjson.Message) (map[string]any, error) {
	data := bytes.TrimSpace(schema)
	if len(data) == 0 {
		return nil, errors.New("schema JSON is required")
	}
	if !json.Valid(data) {
		return nil, errors.New("schema must be valid JSON")
	}
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		return nil, errors.New("schema JSON must be an object")
	}
	for name, value := range decoded {
		value, err := smithyDocumentValue(value)
		if err != nil {
			return nil, err
		}
		decoded[name] = value
	}
	return decoded, nil
}

// smithyDocumentValue preserves canonical JSON numbers using the exact type
// recognized by the AWS document encoder instead of letting json.Number encode
// as a JSON string.
func smithyDocumentValue(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		if !strings.ContainsAny(v.String(), ".eE") {
			integer, ok := new(big.Int).SetString(v.String(), 10)
			if !ok {
				return nil, fmt.Errorf("invalid JSON integer %q", v)
			}
			return integer, nil
		}
		decimal, _, err := big.ParseFloat(
			v.String(),
			10,
			uint(max(64, len(v.String())*4)),
			big.ToNearestEven,
		)
		if err != nil {
			return nil, fmt.Errorf("invalid JSON number %q: %w", v, err)
		}
		return decimal, nil
	case []any:
		for i, item := range v {
			item, err := smithyDocumentValue(item)
			if err != nil {
				return nil, err
			}
			v[i] = item
		}
		return v, nil
	case map[string]any:
		for name, item := range v {
			item, err := smithyDocumentValue(item)
			if err != nil {
				return nil, err
			}
			v[name] = item
		}
		return v, nil
	default:
		return value, nil
	}
}

// translateResponse converts a Bedrock Converse output into the canonical
// model.Response, mapping provider tool names back to canonical identifiers
// via nameMap and stamping the resolved model identifier and class.
func translateResponse(output *bedrockruntime.ConverseOutput, nameMap map[string]string, modelID string, modelClass model.ModelClass) (*model.Response, error) {
	if output == nil {
		return nil, errors.New("bedrock: response is nil")
	}
	resp := &model.Response{}
	msg, ok := output.Output.(*brtypes.ConverseOutputMemberMessage)
	if !ok {
		return nil, fmt.Errorf("bedrock: unsupported response output %T", output.Output)
	}
	assistant := model.Message{Role: model.ConversationRole(msg.Value.Role)}
	for _, block := range msg.Value.Content {
		switch v := block.(type) {
		case *brtypes.ContentBlockMemberReasoningContent:
			switch reasoning := v.Value.(type) {
			case *brtypes.ReasoningContentBlockMemberReasoningText:
				text := aws.ToString(reasoning.Value.Text)
				signature := aws.ToString(reasoning.Value.Signature)
				if signature == "" {
					return nil, errors.New("bedrock: response reasoning plaintext is missing provider signature")
				}
				// Signature-only reasoning blocks are canonical when thinking
				// display is "omitted"; preserve them for verbatim replay.
				assistant.Parts = append(assistant.Parts, model.ThinkingPart{
					Text:      text,
					Signature: signature,
					Final:     true,
				})
			case *brtypes.ReasoningContentBlockMemberRedactedContent:
				if len(reasoning.Value) == 0 {
					return nil, errors.New("bedrock: response redacted reasoning block requires data")
				}
				assistant.Parts = append(assistant.Parts, model.ThinkingPart{
					Redacted: append([]byte(nil), reasoning.Value...),
					Final:    true,
				})
			default:
				return nil, fmt.Errorf("bedrock: unsupported response reasoning block %T", v.Value)
			}
		case *brtypes.ContentBlockMemberText:
			assistant.Parts = append(assistant.Parts, model.TextPart{Text: v.Value})
		case *brtypes.ContentBlockMemberCitationsContent:
			part, err := translateCitationsContent(v.Value)
			if err != nil {
				return nil, err
			}
			assistant.Parts = append(assistant.Parts, part)
		case *brtypes.ContentBlockMemberToolUse:
			payload, err := decodeDocument(v.Value.Input)
			if err != nil {
				return nil, fmt.Errorf("bedrock: decode tool use input: %w", err)
			}
			if v.Value.Name == nil || *v.Value.Name == "" {
				return nil, errors.New("bedrock: response tool use block missing name")
			}
			raw := *v.Value.Name
			key := normalizeToolName(raw)
			name, ok := nameMap[key]
			if !ok {
				return nil, fmt.Errorf(
					"bedrock: response tool use block returned unadvertised name %q",
					raw,
				)
			}
			if v.Value.ToolUseId == nil || *v.Value.ToolUseId == "" {
				return nil, errors.New("bedrock: response tool use block missing ID")
			}
			assistant.Parts = append(assistant.Parts, model.ToolUsePart{
				Name:  string(tools.Ident(name)),
				Input: payload,
				ID:    *v.Value.ToolUseId,
			})
		default:
			return nil, fmt.Errorf("bedrock: unsupported response content block %T", block)
		}
	}
	if len(assistant.Parts) > 0 {
		resp.Content = append(resp.Content, assistant)
	}
	resp.Usage = translateBedrockUsage(output, modelID, modelClass)
	resp.StopReason = string(output.StopReason)
	if resp.StopReason == "" {
		return nil, errors.New("bedrock: response is missing its stop reason")
	}
	return resp, nil
}

// translateBedrockUsage extracts provider usage and resolved model identity
// before content translation so malformed content cannot erase valid billing
// evidence.
func translateBedrockUsage(output *bedrockruntime.ConverseOutput, modelID string, modelClass model.ModelClass) model.TokenUsage {
	usage := model.TokenUsage{
		Model:      modelID,
		ModelClass: modelClass,
	}
	if output == nil || output.Usage == nil {
		return usage
	}
	usage.InputTokens = int(ptrValue(output.Usage.InputTokens))
	usage.OutputTokens = int(ptrValue(output.Usage.OutputTokens))
	usage.TotalTokens = int(ptrValue(output.Usage.TotalTokens))
	usage.CacheReadTokens = int(ptrValue(output.Usage.CacheReadInputTokens))
	usage.CacheWriteTokens = int(ptrValue(output.Usage.CacheWriteInputTokens))
	return usage
}

func decodeDocument(doc document.Interface) (rawjson.Message, error) {
	if doc == nil {
		return nil, errors.New("document is nil")
	}
	data, err := doc.MarshalSmithyDocument()
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("document is empty")
	}
	return rawjson.Message(data), nil
}

func translateCitationsContent(block brtypes.CitationsContentBlock) (model.CitationsPart, error) {
	var b strings.Builder
	for _, content := range block.Content {
		switch v := content.(type) {
		case *brtypes.CitationGeneratedContentMemberText:
			b.WriteString(v.Value)
		default:
			return model.CitationsPart{}, fmt.Errorf("bedrock: unsupported citation generated content %T", content)
		}
	}
	citations := make([]model.Citation, 0, len(block.Citations))
	for _, c := range block.Citations {
		citation, err := translateCitation(c)
		if err != nil {
			return model.CitationsPart{}, err
		}
		citations = append(citations, citation)
	}
	return model.CitationsPart{
		Text:      b.String(),
		Citations: citations,
	}, nil
}

func translateCitation(c brtypes.Citation) (model.Citation, error) {
	location, err := translateCitationLocation(c.Location)
	if err != nil {
		return model.Citation{}, err
	}
	sourceContent, err := translateCitationSourceContent(c.SourceContent)
	if err != nil {
		return model.Citation{}, err
	}
	out := model.Citation{
		Location:      location,
		SourceContent: sourceContent,
	}
	if c.Title != nil {
		out.Title = *c.Title
	}
	if c.Source != nil {
		out.Source = *c.Source
	}
	return out, nil
}

func translateCitationLocation(loc brtypes.CitationLocation) (model.CitationLocation, error) {
	switch v := loc.(type) {
	case *brtypes.CitationLocationMemberDocumentChar:
		return model.CitationLocation{
			DocumentChar: &model.DocumentCharLocation{
				DocumentIndex: int(ptrValue(v.Value.DocumentIndex)),
				Start:         int(ptrValue(v.Value.Start)),
				End:           int(ptrValue(v.Value.End)),
			},
		}, nil
	case *brtypes.CitationLocationMemberDocumentChunk:
		return model.CitationLocation{
			DocumentChunk: &model.DocumentChunkLocation{
				DocumentIndex: int(ptrValue(v.Value.DocumentIndex)),
				Start:         int(ptrValue(v.Value.Start)),
				End:           int(ptrValue(v.Value.End)),
			},
		}, nil
	case *brtypes.CitationLocationMemberDocumentPage:
		return model.CitationLocation{
			DocumentPage: &model.DocumentPageLocation{
				DocumentIndex: int(ptrValue(v.Value.DocumentIndex)),
				Start:         int(ptrValue(v.Value.Start)),
				End:           int(ptrValue(v.Value.End)),
			},
		}, nil
	default:
		return model.CitationLocation{}, fmt.Errorf("bedrock: unsupported citation location %T", loc)
	}
}

func translateCitationSourceContent(contents []brtypes.CitationSourceContent) ([]string, error) {
	if len(contents) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(contents))
	for _, content := range contents {
		switch v := content.(type) {
		case *brtypes.CitationSourceContentMemberText:
			out = append(out, v.Value)
		default:
			return nil, fmt.Errorf("bedrock: unsupported citation source content %T", content)
		}
	}
	return out, nil
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

func hasToolDefinition(defs []*model.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def == nil {
			continue
		}
		if def.Name == name {
			return true
		}
	}
	return false
}

// isNovaModel reports whether the given model identifier refers to an Amazon
// Nova family model. Nova models do not currently support tool-level cache
// checkpoints in the tool configuration.
func isNovaModel(modelID string) bool {
	if modelID == "" {
		return false
	}
	// Bedrock Nova models are prefixed with "amazon.nova-".
	return strings.HasPrefix(modelID, "amazon.nova-")
}

func isAnthropicBedrockModel(modelID string) bool {
	return strings.HasPrefix(modelID, "anthropic.") ||
		strings.Contains(modelID, ".anthropic.")
}

// messagesHaveToolBlocks returns true if any message in the slice contains
// a ToolUsePart or ToolResultPart. Bedrock requires toolConfig to be set
// when such parts are present.
func messagesHaveToolBlocks(msgs []*model.Message) bool {
	for _, m := range msgs {
		if m == nil {
			continue
		}
		for _, p := range m.Parts {
			switch p.(type) {
			case model.ToolUsePart, model.ToolResultPart:
				return true
			}
		}
	}
	return false
}
