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
	"net/http"
	"strings"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const (
	defaultThinkingBudget = 16384
	bedrockProviderName   = "bedrock"
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
	// MaxTokens. When zero or negative, the client omits MaxTokens so Bedrock
	// uses its own default.
	MaxTokens int

	// Temperature is used when a request does not specify Temperature.
	Temperature float32

	// ThinkingBudget defines the thinking token budget when thinking is enabled
	// for streaming calls. When zero or negative, the client omits
	// budget_tokens so Bedrock uses its own default budget.
	ThinkingBudget int

	// Logger is used for non-fatal diagnostics inside the Bedrock adapter.
	// When nil, defaults to a no-op logger.
	Logger telemetry.Logger
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
	logger       telemetry.Logger
}

// ledgerSource provides provider-ready messages for a given run when available.
// This interface is internal to the goa-ai bedrock client and implemented by the
// runtime using engine-specific mechanisms (e.g., Temporal workflow queries).
type ledgerSource interface {
	Messages(ctx context.Context, runID string) ([]*model.Message, error)
}

type requestParts struct {
	modelID                 string
	modelClass              model.ModelClass
	messages                []brtypes.Message
	system                  []brtypes.SystemContentBlock
	toolConfig              *brtypes.ToolConfiguration
	toolNameCanonicalToProv map[string]string
	toolNameProvToCanonical map[string]string
}

type thinkingConfig struct {
	enable      bool
	interleaved bool
	budget      int
}

// New initializes a Bedrock-powered model client configured for chat completion
// and streaming requests. The provided ledgerSource allows the client to prepend
// provider-verified messages for a specific run ID during request encoding,
// ensuring transcript continuity across completions.
func New(aws *bedrockruntime.Client, opts Options, ledger ledgerSource) (*Client, error) {
	opts.Runtime = aws
	if opts.Runtime == nil {
		return nil, errors.New("bedrock runtime client is required")
	}
	// DefaultModel should be provided; High/Small are optional but recommended.
	if opts.DefaultModel == "" {
		return nil, errors.New("default model identifier is required")
	}
	maxTokens := opts.MaxTokens
	thinkBudget := opts.ThinkingBudget
	if thinkBudget <= 0 {
		thinkBudget = defaultThinkingBudget
	}
	logger := opts.Logger
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}
	c := &Client{
		runtime:      opts.Runtime,
		ledger:       ledger,
		defaultModel: opts.DefaultModel,
		highModel:    opts.HighModel,
		smallModel:   opts.SmallModel,
		maxTok:       maxTokens,
		temp:         opts.Temperature,
		think:        thinkBudget,
		logger:       logger,
	}
	return c, nil
}

// Complete issues a chat completion request to the configured Bedrock model
// using the Converse API and translates the response into planner-friendly
// structures (assistant messages + tool calls).
func (c *Client) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	parts, err := c.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	output, err := c.runtime.Converse(ctx, c.buildConverseInput(parts, req))
	if err != nil {
		if isRateLimited(err) {
			return nil, fmt.Errorf("%w: %w", model.ErrRateLimited, err)
		}
		return nil, wrapBedrockError("converse", err)
	}
	return translateResponse(output, parts.toolNameProvToCanonical, parts.modelID, parts.modelClass)
}

// Stream invokes the Bedrock ConverseStream API and adapts incremental events
// into model.Chunks so planners can surface partial responses.
func (c *Client) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	parts, err := c.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	thinking := c.resolveThinking(req, parts)
	input := c.buildConverseStreamInput(parts, req, thinking)
	out, err := c.runtime.ConverseStream(ctx, input, c.streamOptions(thinking)...)
	if err != nil {
		if isRateLimited(err) {
			return nil, fmt.Errorf("%w: %w", model.ErrRateLimited, err)
		}
		return nil, wrapBedrockError("converse_stream", err)
	}
	stream := out.GetStream()
	if stream == nil {
		return nil, errors.New("bedrock: stream output missing event stream")
	}
	return newBedrockStreamer(ctx, stream, parts.toolNameProvToCanonical, parts.modelID, parts.modelClass), nil
}

func (c *Client) prepareRequest(ctx context.Context, req *model.Request) (*requestParts, error) {
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
			"bedrock: Cache.AfterTools is not supported for Nova models (run=%s, model=%s)",
			req.RunID, modelID,
		)
	}
	// Build tool configuration and name maps before encoding messages so tool_use
	// names can reuse the exact sanitized identifiers. encodeTools is the single
	// source of truth for name sanitization.
	toolConfig, canonToSan, sanToCanon, err := encodeTools(ctx, req.Tools, req.ToolChoice, cacheAfterTools, c.logger)
	if err != nil {
		return nil, err
	}
	// Bedrock requires toolConfig when messages contain tool_use or tool_result
	// blocks. Fail fast with a clear error rather than letting Bedrock reject
	// the request with a generic validation error.
	if toolConfig == nil && messagesHaveToolBlocks(merged) {
		return nil, fmt.Errorf(
			"bedrock: messages contain tool_use/tool_result but no tools provided in request (run=%s); "+
				"ensure the planner always passes tools when history has tool blocks",
			req.RunID,
		)
	}
	messages, system, err := encodeMessages(ctx, merged, canonToSan, cacheAfterSystem, c.logger)
	if err != nil {
		return nil, err
	}
	return &requestParts{
		modelID:                 modelID,
		modelClass:              req.ModelClass,
		messages:                messages,
		system:                  system,
		toolConfig:              toolConfig,
		toolNameCanonicalToProv: canonToSan,
		toolNameProvToCanonical: sanToCanon,
	}, nil
}

// resolveModelID decides which concrete model ID to use based on Request.Model
// and Request.ModelClass. Request.Model takes precedence; when empty, the class
// is mapped to the configured identifiers. Falls back to the default model.
func (c *Client) resolveModelID(req *model.Request) string {
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

func (c *Client) buildConverseInput(parts *requestParts, req *model.Request) *bedrockruntime.ConverseInput {
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

func (c *Client) buildConverseStreamInput(parts *requestParts, req *model.Request, thinking thinkingConfig) *bedrockruntime.ConverseStreamInput {
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
		thinkingCfg := map[string]any{
			"type": "enabled",
		}
		if thinking.budget > 0 {
			thinkingCfg["budget_tokens"] = thinking.budget
		}
		fields := map[string]any{
			"thinking": thinkingCfg,
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

func (c *Client) resolveThinking(req *model.Request, parts *requestParts) thinkingConfig {
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

// isRateLimited reports whether err represents a provider rate limiting
// condition. It treats both HTTP 429 responses and provider error codes like
// ThrottlingException as rate-limited signals and is idempotent when
// ErrRateLimited is already present in the error chain.
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, model.ErrRateLimited) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ThrottlingException", "TooManyRequestsException":
			return true
		}
	}
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == 429 {
		return true
	}

	return false
}

func wrapBedrockError(operation string, err error) error {
	if isRateLimited(err) {
		pe := model.NewProviderError(bedrockProviderName, operation, http.StatusTooManyRequests, model.ProviderErrorKindRateLimited, "rate_limited", "", "", true, err)
		return errors.Join(model.ErrRateLimited, pe)
	}

	var (
		status int
		code   string
		msg    string
	)

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code = apiErr.ErrorCode()
		msg = apiErr.ErrorMessage()
	}
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		status = respErr.HTTPStatusCode()
	}

	kind := model.ProviderErrorKindUnknown
	retryable := false
	switch {
	case status == http.StatusBadRequest:
		kind = model.ProviderErrorKindInvalidRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = model.ProviderErrorKindAuth
	case status == http.StatusTooManyRequests:
		kind = model.ProviderErrorKindRateLimited
		retryable = true
	case status >= http.StatusInternalServerError && status <= http.StatusNetworkAuthenticationRequired:
		kind = model.ProviderErrorKindUnavailable
		retryable = true
	}

	return model.NewProviderError(bedrockProviderName, operation, status, kind, code, msg, "", retryable, err)
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

func encodeMessages(ctx context.Context, msgs []*model.Message, nameMap map[string]string, cacheAfterSystem bool, logger telemetry.Logger) ([]brtypes.Message, []brtypes.SystemContentBlock, error) {
	// toolUseIDMap tracks a per-request mapping from canonical tool_use IDs used
	// in transcripts (which may be long or contain slashes) to provider-safe
	// IDs that conform to Bedrock constraints ([a-zA-Z0-9_-]+, <=64 chars). The
	// mapping is local to this encode pass; it is not persisted or surfaced to
	// callers. This ensures we never send internal correlation IDs (for example,
	// long RunID-based strings) as Bedrock toolUseId values.
	toolUseIDMap := make(map[string]string)
	nextToolUseID := 0

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
				case model.CacheCheckpointPart:
					system = append(system, &brtypes.SystemContentBlockMemberCachePoint{
						Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault},
					})
				case model.DocumentPart:
					return nil, nil, errors.New("bedrock: document parts are not supported in system messages")
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
				// When the caller requests citations, we honor it only for inline sources.
				if v.Cite && !isS3Source {
					doc.Citations = &brtypes.CitationsConfig{Enabled: aws.Bool(true)}
				}
				if v.Context != "" {
					doc.Context = aws.String(v.Context)
				}
				blocks = append(blocks, &brtypes.ContentBlockMemberDocument{Value: doc})
			case model.ToolUsePart:
				// Encode assistant-declared tool_use with optional ID and JSON input.
				tb := brtypes.ToolUseBlock{}
				if v.Name != "" {
					// Strong contract: tool_use names in messages must match tool
					// definitions in the current request. Fail fast when a tool_use
					// references an unknown tool - this indicates transcript
					// contamination (e.g., ledger key collision between agent runs)
					// or a missing tool definition.
					sanitized, ok := nameMap[v.Name]
					if !ok || sanitized == "" {
						return nil, nil, fmt.Errorf(
							"bedrock: tool_use in messages references %q which is not in "+
								"the current tool configuration; ensure transcript and "+
								"tool definitions are aligned (possible ledger contamination)",
							v.Name,
						)
					}
					tb.Name = aws.String(sanitized)
				}
				if v.ID != "" {
					if id := toolUseIDFor(v.ID, toolUseIDMap, &nextToolUseID); id != "" {
						tb.ToolUseId = aws.String(id)
					}
				}
				tb.Input = toDocument(ctx, v.Input, logger)
				blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{Value: tb})
			case model.ToolResultPart:
				// Bedrock expects tool_result blocks in user messages, correlated to a prior tool_use.
				// Encode content as text when Content is a string; otherwise as a JSON document.
				tr := brtypes.ToolResultBlock{}
				if id := toolUseIDFor(v.ToolUseID, toolUseIDMap, &nextToolUseID); id != "" {
					tr.ToolUseId = aws.String(id)
				}
				if s, ok := v.Content.(string); ok {
					tr.Content = []brtypes.ToolResultContentBlock{
						&brtypes.ToolResultContentBlockMemberText{Value: s},
					}
				} else {
					doc := toDocument(ctx, v.Content, logger)
					tr.Content = []brtypes.ToolResultContentBlock{
						&brtypes.ToolResultContentBlockMemberJson{Value: doc},
					}
				}
				blocks = append(blocks, &brtypes.ContentBlockMemberToolResult{Value: tr})
			case model.CacheCheckpointPart:
				blocks = append(blocks, &brtypes.ContentBlockMemberCachePoint{
					Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault},
				})
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

func encodeTools(
	ctx context.Context,
	defs []*model.ToolDefinition,
	choice *model.ToolChoice,
	cacheAfterTools bool,
	logger telemetry.Logger,
) (*brtypes.ToolConfiguration, map[string]string, map[string]string, error) {
	if len(defs) == 0 {
		if choice == nil {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("bedrock: tool choice is set but no tools are defined")
	}
	toolList := make([]brtypes.Tool, 0, len(defs))
	// canonToSan maps canonical IDs (toolset.tool) to provider-visible sanitized names.
	canonToSan := make(map[string]string, len(defs))
	// sanToCanon is the reverse map used to translate provider names back to canonical IDs.
	sanToCanon := make(map[string]string, len(defs))
	for _, def := range defs {
		if def == nil {
			continue
		}
		canonical := def.Name
		if canonical == "" {
			continue
		}
		sanitized := SanitizeToolName(canonical)
		if prev, ok := sanToCanon[sanitized]; ok && prev != canonical {
			return nil, nil, nil, fmt.Errorf(
				"bedrock: tool name %q sanitizes to %q which collides with %q",
				canonical, sanitized, prev,
			)
		}
		sanToCanon[sanitized] = canonical
		canonToSan[canonical] = sanitized
		if def.Description == "" {
			return nil, nil, nil, fmt.Errorf("bedrock: tool %q is missing description", canonical)
		}
		schemaDoc := toDocument(ctx, def.InputSchema, logger)
		spec := brtypes.ToolSpecification{
			Name:        aws.String(sanitized),
			Description: aws.String(def.Description),
			InputSchema: &brtypes.ToolInputSchemaMemberJson{Value: schemaDoc},
		}
		toolList = append(toolList, &brtypes.ToolMemberToolSpec{Value: spec})
	}
	if len(toolList) == 0 {
		if choice == nil || choice.Mode == model.ToolChoiceModeNone {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("bedrock: tool choice is set but no tools are defined")
	}
	// Policy-driven: append a cache checkpoint after tools when requested.
	// Note: Only Claude models support tool-level cache checkpoints; Nova does not.
	if cacheAfterTools {
		toolList = append(toolList, &brtypes.ToolMemberCachePoint{
			Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault},
		})
	}

	if choice == nil {
		return &brtypes.ToolConfiguration{Tools: toolList}, canonToSan, sanToCanon, nil
	}

	cfg := brtypes.ToolConfiguration{
		Tools: toolList,
	}

	switch choice.Mode {
	case "", model.ToolChoiceModeAuto:
		// Auto is the provider default; omit ToolChoice to preserve existing
		// behavior.
	case model.ToolChoiceModeNone:
		// Preserve tool configuration so Bedrock can interpret existing
		// tool_use and tool_result content blocks in the transcript, but do
		// not force additional tool calls. Callers rely on prompts and
		// higher-level contracts to prevent new tool invocations.
	case model.ToolChoiceModeAny:
		cfg.ToolChoice = &brtypes.ToolChoiceMemberAny{
			Value: brtypes.AnyToolChoice{},
		}
	case model.ToolChoiceModeTool:
		if choice.Name == "" {
			return nil, nil, nil, fmt.Errorf("bedrock: tool choice mode %q requires a tool name", choice.Mode)
		}
		if !hasToolDefinition(defs, choice.Name) {
			return nil, nil, nil, fmt.Errorf("bedrock: tool choice name %q does not match any tool", choice.Name)
		}
		sanitized := SanitizeToolName(choice.Name)
		if canonical, ok := sanToCanon[sanitized]; !ok || canonical != choice.Name {
			return nil, nil, nil, fmt.Errorf("bedrock: tool choice name %q does not match any tool", choice.Name)
		}
		cfg.ToolChoice = &brtypes.ToolChoiceMemberTool{
			Value: brtypes.SpecificToolChoice{Name: aws.String(sanitized)},
		}
	default:
		return nil, nil, nil, fmt.Errorf("bedrock: unsupported tool choice mode %q", choice.Mode)
	}

	return &cfg, canonToSan, sanToCanon, nil
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

func toolUseIDFor(canonical string, toolUseIDMap map[string]string, nextToolUseID *int) string {
	if canonical == "" {
		return ""
	}
	if isProviderSafeToolUseID(canonical) {
		return canonical
	}
	if id, ok := toolUseIDMap[canonical]; ok {
		return id
	}
	*nextToolUseID++
	id := fmt.Sprintf("t%d", *nextToolUseID)
	toolUseIDMap[canonical] = id
	return id
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

func toDocument(ctx context.Context, schema any, logger telemetry.Logger) document.Interface {
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}
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
			logger.Error(
				ctx,
				"failed to unmarshal schema",
				"component", "inference-engine",
				"event", "unmarshal_schema_failed",
				"err", err,
			)
			return lazyDocument(map[string]any{"type": "object"})
		}
		return lazyDocument(decoded)
	default:
		return lazyDocument(v)
	}
}

// isProviderSafeToolUseID reports whether id conforms to Bedrock's documented
// toolUseId constraints: pattern [a-zA-Z0-9_-]+ and length <= 64. The check is
// intentionally strict so internal correlation IDs (for example, run-scoped
// paths containing slashes) are never forwarded directly to the provider.
func isProviderSafeToolUseID(id string) bool {
	if id == "" {
		return false
	}
	if len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

func translateResponse(output *bedrockruntime.ConverseOutput, nameMap map[string]string, modelID string, modelClass model.ModelClass) (*model.Response, error) {
	if output == nil {
		return nil, errors.New("bedrock: response is nil")
	}
	resp := &model.Response{}
	if msg, ok := output.Output.(*brtypes.ConverseOutputMemberMessage); ok {
		for _, block := range msg.Value.Content {
			switch v := block.(type) {
			case *brtypes.ContentBlockMemberText:
				if v.Value == "" {
					continue
				}
				resp.Content = append(resp.Content, model.Message{
					Role:  "assistant",
					Parts: []model.Part{model.TextPart{Text: v.Value}},
				})
			case *brtypes.ContentBlockMemberCitationsContent:
				part := translateCitationsContent(v.Value)
				if part.Text == "" && len(part.Citations) == 0 {
					continue
				}
				resp.Content = append(resp.Content, model.Message{
					Role:  "assistant",
					Parts: []model.Part{part},
				})
			case *brtypes.ContentBlockMemberToolUse:
				payload := decodeDocument(v.Value.Input)
				name := ""
				if v.Value.Name != nil {
					raw := *v.Value.Name
					key := normalizeToolName(raw)
					canonical, ok := nameMap[key]
					if !ok {
						return nil, fmt.Errorf(
							"bedrock: tool name %q not in reverse map (raw: %q); expected canonical tool ID",
							key, raw,
						)
					}
					name = canonical
				}
				var id string
				if v.Value.ToolUseId != nil {
					id = *v.Value.ToolUseId
				}
				resp.ToolCalls = append(resp.ToolCalls, model.ToolCall{
					Name:    tools.Ident(name),
					Payload: payload,
					ID:      id,
				})
			}
		}
	}
	if usage := output.Usage; usage != nil {
		resp.Usage = model.TokenUsage{
			Model:            modelID,
			ModelClass:       modelClass,
			InputTokens:      int(ptrValue(usage.InputTokens)),
			OutputTokens:     int(ptrValue(usage.OutputTokens)),
			TotalTokens:      int(ptrValue(usage.TotalTokens)),
			CacheReadTokens:  int(ptrValue(usage.CacheReadInputTokens)),
			CacheWriteTokens: int(ptrValue(usage.CacheWriteInputTokens)),
		}
	}
	resp.StopReason = string(output.StopReason)
	return resp, nil
}

func decodeDocument(doc document.Interface) json.RawMessage {
	if doc == nil {
		return nil
	}
	data, err := doc.MarshalSmithyDocument()
	if err != nil {
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	return json.RawMessage(data)
}

func translateCitationsContent(block brtypes.CitationsContentBlock) model.CitationsPart {
	var b strings.Builder
	for _, content := range block.Content {
		if v, ok := content.(*brtypes.CitationGeneratedContentMemberText); ok {
			b.WriteString(v.Value)
		}
	}
	citations := make([]model.Citation, 0, len(block.Citations))
	for _, c := range block.Citations {
		citations = append(citations, translateCitation(c))
	}
	return model.CitationsPart{
		Text:      b.String(),
		Citations: citations,
	}
}

func translateCitation(c brtypes.Citation) model.Citation {
	out := model.Citation{
		Location:      translateCitationLocation(c.Location),
		SourceContent: translateCitationSourceContent(c.SourceContent),
	}
	if c.Title != nil {
		out.Title = *c.Title
	}
	if c.Source != nil {
		out.Source = *c.Source
	}
	return out
}

func translateCitationLocation(loc brtypes.CitationLocation) model.CitationLocation {
	switch v := loc.(type) {
	case *brtypes.CitationLocationMemberDocumentChar:
		return model.CitationLocation{
			DocumentChar: &model.DocumentCharLocation{
				DocumentIndex: int(ptrValue(v.Value.DocumentIndex)),
				Start:         int(ptrValue(v.Value.Start)),
				End:           int(ptrValue(v.Value.End)),
			},
		}
	case *brtypes.CitationLocationMemberDocumentChunk:
		return model.CitationLocation{
			DocumentChunk: &model.DocumentChunkLocation{
				DocumentIndex: int(ptrValue(v.Value.DocumentIndex)),
				Start:         int(ptrValue(v.Value.Start)),
				End:           int(ptrValue(v.Value.End)),
			},
		}
	case *brtypes.CitationLocationMemberDocumentPage:
		return model.CitationLocation{
			DocumentPage: &model.DocumentPageLocation{
				DocumentIndex: int(ptrValue(v.Value.DocumentIndex)),
				Start:         int(ptrValue(v.Value.Start)),
				End:           int(ptrValue(v.Value.End)),
			},
		}
	default:
		return model.CitationLocation{}
	}
}

func translateCitationSourceContent(contents []brtypes.CitationSourceContent) []string {
	if len(contents) == 0 {
		return nil
	}
	out := make([]string, 0, len(contents))
	for _, content := range contents {
		if v, ok := content.(*brtypes.CitationSourceContentMemberText); ok {
			if v.Value != "" {
				out = append(out, v.Value)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
