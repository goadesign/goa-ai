// Package bedrock provides a model.Client implementation backed by the AWS
// Bedrock Converse API. It mirrors the inference-engine request pipeline used
// in production systems: split system vs. conversational messages, encode tool
// schemas into Bedrock's ToolConfiguration, and translate Converse responses
// (text + tool_use blocks) back into planner-friendly structures.
package bedrock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"goa.design/clue/log"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const (
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
	// MaxTokens. When zero or negative, the client omits MaxTokens so Bedrock
	// uses its own default.
	MaxTokens int

	// Temperature is used when a request does not specify Temperature.
	Temperature float32

	// ThinkingBudget defines the thinking token budget when thinking is enabled
	// for streaming calls. When zero or negative, the client omits
	// budget_tokens so Bedrock uses its own default budget.
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
// This interface is internal to the goa-ai bedrock client and implemented by the
// runtime using engine-specific mechanisms (e.g., Temporal workflow queries).
type ledgerSource interface {
	Messages(ctx context.Context, runID string) ([]*model.Message, error)
}

type requestParts struct {
	modelID                 string
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
	c := &Client{
		runtime:      opts.Runtime,
		ledger:       ledger,
		defaultModel: opts.DefaultModel,
		highModel:    opts.HighModel,
		smallModel:   opts.SmallModel,
		maxTok:       maxTokens,
		temp:         opts.Temperature,
		think:        thinkBudget,
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
		return nil, fmt.Errorf("bedrock converse: %w", err)
	}
	return translateResponse(output, parts.toolNameProvToCanonical)
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
		return nil, fmt.Errorf("bedrock converse stream: %w", err)
	}
	stream := out.GetStream()
	if stream == nil {
		return nil, errors.New("bedrock: stream output missing event stream")
	}
	return newBedrockStreamer(ctx, stream, parts.toolNameProvToCanonical), nil
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
	toolConfig, canonToSan, sanToCanon, err := encodeTools(ctx, req.Tools, req.ToolChoice, cacheAfterTools)
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
	messages, system, err := encodeMessages(ctx, merged, canonToSan, cacheAfterSystem)
	if err != nil {
		return nil, err
	}
	return &requestParts{
		modelID:                 modelID,
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

func encodeMessages(ctx context.Context, msgs []*model.Message, nameMap map[string]string, cacheAfterSystem bool) ([]brtypes.Message, []brtypes.SystemContentBlock, error) {
	// toolUseIDMap tracks a per-request mapping from canonical tool_use IDs used
	// in transcripts (which may be long or contain slashes) to provider-safe
	// IDs that conform to Bedrock constraints ([a-zA-Z0-9_-]+, <=64 chars). The
	// mapping is local to this encode pass; it is not persisted or surfaced to
	// callers. This ensures we never send internal correlation IDs (for example,
	// long RunID-based strings) as Bedrock toolUseId values.
	toolUseIDMap := make(map[string]string)
	nextToolUseID := 0

	toolUseIDFor := func(canonical string) string {
		if canonical == "" {
			return ""
		}
		if isProviderSafeToolUseID(canonical) {
			return canonical
		}
		if id, ok := toolUseIDMap[canonical]; ok {
			return id
		}
		nextToolUseID++
		id := fmt.Sprintf("t%d", nextToolUseID)
		toolUseIDMap[canonical] = id
		return id
	}

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
					if id := toolUseIDFor(v.ID); id != "" {
						tb.ToolUseId = aws.String(id)
					}
				}
				tb.Input = toDocument(ctx, v.Input)
				blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{Value: tb})
			case model.ToolResultPart:
				// Bedrock expects tool_result blocks in user messages, correlated to a prior tool_use.
				// Encode content as text when Content is a string; otherwise as a JSON document.
				tr := brtypes.ToolResultBlock{}
				if id := toolUseIDFor(v.ToolUseID); id != "" {
					tr.ToolUseId = aws.String(id)
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
		sanitized := sanitizeToolName(canonical)
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
		schemaDoc := toDocument(ctx, def.InputSchema)
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
		sanitized := sanitizeToolName(choice.Name)
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

// sanitizeToolName maps a canonical tool identifier to characters allowed by
// the Bedrock constraint [a-zA-Z0-9_-]+ by replacing any disallowed rune with
// '_'. Unlike OpenAI-style providers, Bedrock imposes stricter constraints on
// tool names and some models/providers surface only the tool name string in tool
// use blocks.
//
// Contract:
//   - The mapping must be deterministic and collision-free within a request.
//   - The mapping must preserve namespace information from canonical IDs so two
//     different tools cannot sanitize to the same provider-visible name.
//
// Canonical tool identifiers use dot-separated namespaces (e.g. "toolset.tool"
// or "atlas.read.get_time_series"). We keep the full canonical ID, replace '.'
// with '_', and apply the Bedrock rune constraint. If the sanitized name would
// exceed Bedrock's documented 64-character limit, we truncate and append a short
// stable hash suffix derived from the canonical ID.
func sanitizeToolName(in string) string {
	if in == "" {
		return ""
	}
	const maxLen = 64
	const hashLen = 8

	// Fast path: if all runes are already allowed after mapping '.' to '_', keep
	// the string allocation-free.
	allowed := true
	for _, r := range in {
		if r == '.' {
			r = '_'
		}
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		case r == '-':
		default:
			allowed = false
		}
		if !allowed {
			break
		}
	}

	var sanitized string
	if allowed {
		sanitized = strings.ReplaceAll(in, ".", "_")
	} else {
		out := make([]rune, 0, len(in))
		for _, r := range in {
			if r == '.' {
				r = '_'
			}
			switch {
			case r >= 'a' && r <= 'z':
				out = append(out, r)
			case r >= 'A' && r <= 'Z':
				out = append(out, r)
			case r >= '0' && r <= '9':
				out = append(out, r)
			case r == '_' || r == '-':
				out = append(out, r)
			default:
				out = append(out, '_')
			}
		}
		sanitized = string(out)
	}

	if len(sanitized) <= maxLen {
		return sanitized
	}

	// Truncate and append a stable hash suffix to keep names within Bedrock's
	// documented 64-character limit while preserving uniqueness.
	sum := sha256.Sum256([]byte(in))
	suffix := hex.EncodeToString(sum[:])[:hashLen]

	// Reserve "_" + hashLen at the end.
	prefixLen := maxLen - (1 + hashLen)
	if prefixLen < 1 {
		prefixLen = 1
	}
	return sanitized[:prefixLen] + "_" + suffix
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

func translateResponse(output *bedrockruntime.ConverseOutput, nameMap map[string]string) (*model.Response, error) {
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
