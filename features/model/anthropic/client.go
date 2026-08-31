// Package anthropic provides raw and validated adapters backed by the Anthropic
// Claude Messages API. It translates goa-ai requests into
// anthropic.Message calls using github.com/anthropics/anthropic-sdk-go and maps
// responses (text, tools, thinking, usage) back into the generic planner
// structures.
package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"goa.design/goa-ai/features/model/internal/claudebeta"
	"goa.design/goa-ai/features/model/internal/claudecaps"
	"goa.design/goa-ai/features/model/internal/modelid"
	"goa.design/goa-ai/features/model/internal/outputvalidation"
	"goa.design/goa-ai/features/model/internal/tooluseid"
	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// MessagesClient captures the subset of the Anthropic SDK client used by the
	// adapter. It is satisfied by *sdk.MessageService so callers can pass either a
	// real client or a mock in tests.
	MessagesClient interface {
		New(ctx context.Context, body sdk.MessageNewParams, opts ...option.RequestOption) (*sdk.Message, error)
		NewStreaming(ctx context.Context, body sdk.MessageNewParams, opts ...option.RequestOption) *ssestream.Stream[sdk.MessageStreamEventUnion]
		CountTokens(ctx context.Context, body sdk.MessageCountTokensParams, opts ...option.RequestOption) (*sdk.MessageTokensCount, error)
	}

	// encodedRequest is the canonical Anthropic encoding shared by every call
	// path: resolved model, encoded transcript, tools with their per-request
	// name maps, and the request options any provider-native contract in the
	// encoding requires. Completion policy (max_tokens, temperature, thinking)
	// is layered on top by completionParams; CountTokens consumes the encoding
	// directly, which is what keeps counting free of completion-only
	// requirements. Request options travel with the encoding so a contract can
	// never be separated from the fields that require it: tools carrying
	// input_examples are only legal when the tool-examples beta is active, and
	// bundling them here makes it impossible for a call path to send one
	// without the other.
	encodedRequest struct {
		model           string
		messages        []sdk.MessageParam
		system          []sdk.TextBlockParam
		tools           []sdk.ToolUnionParam
		toolChoice      sdk.ToolChoiceUnionParam
		outputConfig    sdk.OutputConfigParam
		provToCanon     map[string]string
		noArgumentTools map[string]struct{}
		opts            []option.RequestOption
	}

	// Options configures optional Anthropic adapter behavior.
	Options struct {
		// DefaultModel is the default Claude model identifier used when
		// model.Request.Model is empty. Use the typed model constants from
		// github.com/anthropics/anthropic-sdk-go (for example,
		// string(sdk.ModelClaudeSonnet4_5_20250929)) or the identifiers listed in
		// the Anthropic model reference in their docs/console.
		DefaultModel string

		// HighModel is the high-reasoning model identifier used when
		// model.Request.ModelClass is ModelClassHighReasoning and Model is empty.
		// As with DefaultModel, prefer the anthropic-sdk-go Model constants or the
		// IDs from Anthropic's model catalogue.
		HighModel string

		// SmallModel is the small/cheap model identifier used when
		// model.Request.ModelClass is ModelClassSmall and Model is empty. Source
		// identifiers from the anthropic-sdk-go Model constants or Anthropic's
		// model documentation.
		SmallModel string

		// MaxTokens sets the default completion cap when a request does not specify
		// MaxTokens. When zero or negative, the client requires callers to set
		// Request.MaxTokens explicitly.
		MaxTokens int

		// Temperature is used when a request does not specify Temperature.
		// It is silently omitted from the wire request for models that no
		// longer accept the parameter (Claude Opus 4.7+, Claude Sonnet 5+,
		// and the Fable/Mythos generation) — see
		// features/model/internal/claudecaps.TemperatureSupported for the
		// exact rule. Those models run at their own default sampling
		// behavior regardless of this setting; the omission is recorded on
		// the ambient trace span.
		Temperature float64

		// ThinkingBudget defines the default thinking token budget when thinking is
		// enabled. When zero or negative, callers must supply
		// Request.Thinking.BudgetTokens explicitly.
		ThinkingBudget int64

		// ToolExamplesInSchema keeps authored tool examples as root JSON Schema
		// annotations instead of sending Anthropic's input_examples field. Set
		// this for Messages-compatible endpoints that reject input_examples,
		// such as Amazon Bedrock.
		ToolExamplesInSchema bool
	}

	// provider translates canonical requests to Anthropic Claude Messages.
	provider struct {
		msg                  MessagesClient
		defaultModel         string
		highModel            string
		smallModel           string
		maxTok               int
		temp                 float64
		think                int64
		toolExamplesInSchema bool
	}
)

// anthropicProviderName identifies this adapter in model.ProviderError values.
const anthropicProviderName = "anthropic"

var (
	_ model.Provider     = (*provider)(nil)
	_ model.TokenCounter = (*provider)(nil)
)

// New builds a validated Anthropic-backed model client.
func New(msg MessagesClient, opts Options) (model.Client, error) {
	raw, err := NewProvider(msg, opts)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// NewProvider builds the raw Anthropic provider used beneath model.NewClient.
// Callers use it to install provider-side middleware before final validation.
func NewProvider(msg MessagesClient, opts Options) (model.Provider, error) {
	if msg == nil {
		return nil, errors.New("anthropic client is required")
	}
	if opts.DefaultModel == "" {
		return nil, errors.New("default model identifier is required")
	}
	if opts.MaxTokens < 0 {
		return nil, errors.New("anthropic: default max tokens cannot be negative")
	}
	if opts.ThinkingBudget < 0 {
		return nil, errors.New("anthropic: default thinking budget cannot be negative")
	}
	if err := validateAnthropicTemperature(opts.Temperature); err != nil {
		return nil, fmt.Errorf("anthropic: default %w", err)
	}
	maxTokens := opts.MaxTokens
	thinkBudget := opts.ThinkingBudget

	c := &provider{
		msg:                  msg,
		defaultModel:         opts.DefaultModel,
		highModel:            opts.HighModel,
		smallModel:           opts.SmallModel,
		maxTok:               maxTokens,
		temp:                 opts.Temperature,
		think:                thinkBudget,
		toolExamplesInSchema: opts.ToolExamplesInSchema,
	}
	return c, nil
}

// NewFromAPIKey constructs a validated client using the default Anthropic HTTP
// client.
// It reads ANTHROPIC_API_KEY and related defaults from the environment via
// sdk.DefaultClientOptions.
func NewFromAPIKey(apiKey, defaultModel string) (model.Client, error) {
	raw, err := NewProviderFromAPIKey(apiKey, defaultModel)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// NewProviderFromAPIKey constructs a raw provider using the default Anthropic
// HTTP client.
func NewProviderFromAPIKey(apiKey, defaultModel string) (model.Provider, error) {
	if apiKey == "" {
		return nil, errors.New("api key is required")
	}
	ac := sdk.NewClient(option.WithAPIKey(apiKey))
	return NewProvider(&ac.Messages, Options{DefaultModel: defaultModel})
}

// Complete returns one fully assembled Messages response. It uses Anthropic's
// non-streaming transport when the SDK admits the encoded request and drains
// the streaming transport when the request's output allowance requires it.
func (c *provider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	enc, err := c.encodeRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	params, err := c.completionParams(ctx, req, enc)
	if err != nil {
		return nil, err
	}
	if _, err := sdk.CalculateNonStreamingTimeout(int(params.MaxTokens), params.Model, enc.opts); err != nil {
		stream, streamErr := c.openPreparedStream(ctx, req, contract, enc, params)
		if streamErr != nil {
			return nil, streamErr
		}
		return completeFromAnthropicStream(stream)
	}
	msg, err := c.msg.New(ctx, *params, enc.opts...)
	if err != nil {
		return nil, wrapAnthropicError("complete", err)
	}
	response, err := translateResponse(msg, enc.provToCanon)
	if err != nil {
		usage := translateAnthropicUsage(msg, enc.model, req.ModelClass)
		return nil, contract.RejectProviderOutput(
			outputvalidation.RequiredKind(err),
			&usage,
			err,
		)
	}
	response.Usage = translateAnthropicUsage(msg, enc.model, req.ModelClass)
	return response, nil
}

// CountTokens asks Anthropic to count the exact request fields that inference
// would receive, including saved reasoning, output format, and thinking mode.
func (c *provider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	if _, err := model.NewRequestContract(req); err != nil {
		return model.TokenCount{}, err
	}
	enc, err := c.encodeRequest(ctx, req)
	if err != nil {
		return model.TokenCount{}, err
	}
	thinking, err := c.thinkingConfig(req, enc.model, c.effectiveMaxTokens(req.MaxTokens))
	if err != nil {
		return model.TokenCount{}, err
	}
	countParams := sdk.MessageCountTokensParams{
		Messages:     enc.messages,
		Model:        enc.model,
		OutputConfig: enc.outputConfig,
		Thinking:     thinking,
		ToolChoice:   enc.toolChoice,
	}
	if len(enc.system) > 0 {
		countParams.System = sdk.MessageCountTokensParamsSystemUnion{
			OfTextBlockArray: enc.system,
		}
	}
	if len(enc.tools) > 0 {
		countTools := make([]sdk.MessageCountTokensToolUnionParam, len(enc.tools))
		for i, tool := range enc.tools {
			countTools[i] = sdk.MessageCountTokensToolUnionParam{OfTool: tool.OfTool}
		}
		countParams.Tools = countTools
	}
	count, err := c.msg.CountTokens(ctx, countParams, enc.opts...)
	if err != nil {
		return model.TokenCount{}, wrapAnthropicError("count_tokens", err)
	}
	return model.TokenCount{
		Model:       enc.model,
		ModelClass:  req.ModelClass,
		InputTokens: int(count.InputTokens),
		Exact:       true,
	}, nil
}

// Stream invokes Messages.NewStreaming and adapts incremental events into
// model.Chunks so planners can surface partial responses.
func (c *provider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	enc, err := c.encodeRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	params, err := c.completionParams(ctx, req, enc)
	if err != nil {
		return nil, err
	}
	return c.openPreparedStream(ctx, req, contract, enc, params)
}

// openPreparedStream starts an Anthropic Messages stream after the shared
// request validation and encoding have succeeded.
func (c *provider) openPreparedStream(
	ctx context.Context,
	req *model.Request,
	contract *model.RequestContract,
	enc *encodedRequest,
	params *sdk.MessageNewParams,
) (model.Streamer, error) {
	stream := c.msg.NewStreaming(ctx, *params, enc.opts...)
	if stream == nil {
		return nil, errors.New("anthropic: stream is nil")
	}
	if err := stream.Err(); err != nil {
		return nil, errors.Join(wrapAnthropicError("stream", err), stream.Close())
	}
	return newAnthropicStreamer(
		ctx,
		stream,
		enc.provToCanon,
		enc.noArgumentTools,
		enc.model,
		req.ModelClass,
		req.StructuredOutput,
		contract,
	), nil
}

// completeFromAnthropicStream drains the provider stream without exposing its
// chunks and returns the response that became final at clean EOF.
func completeFromAnthropicStream(stream model.Streamer) (*model.Response, error) {
	for {
		_, recvErr := stream.Recv()
		// Only literal EOF completes the response. A wrapped EOF is a provider
		// failure under the model.Streamer contract.
		//nolint:errorlint // Exact equality is required by the stream contract.
		if recvErr == io.EOF {
			response := stream.Response()
			if err := stream.Close(); err != nil {
				return nil, err
			}
			return response, nil
		}
		if recvErr != nil {
			return nil, errors.Join(recvErr, stream.Close())
		}
	}
}

// encodeRequest builds the canonical Anthropic encoding of req: resolved
// model, transcript, tools (with cache markers and per-request name maps),
// tool choice, and the request options the encoded contracts require. It
// validates only what every call path needs; completion-only requirements
// live in completionParams.
func (c *provider) encodeRequest(ctx context.Context, req *model.Request) (*encodedRequest, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("anthropic: messages are required")
	}
	modelID, err := modelid.Resolve("anthropic", req, c.defaultModel, c.highModel, c.smallModel)
	if err != nil {
		return nil, err
	}
	var outputConfig sdk.OutputConfigParam
	if req.StructuredOutput != nil {
		if !claudecaps.StructuredOutputSupported(modelID) {
			return nil, fmt.Errorf(
				"anthropic: model %q does not support structured output: %w",
				modelID,
				model.ErrStructuredOutputUnsupported,
			)
		}
		schema, err := decodeToolJSONObject(bytes.TrimSpace(req.StructuredOutput.Schema))
		if err != nil {
			return nil, fmt.Errorf("anthropic: structured output schema: %w", err)
		}
		outputConfig.Format = sdk.JSONOutputFormatParam{Schema: schema}
	}
	if forcesToolUse(req.ToolChoice) && claudecaps.ForcedToolChoiceUnsupported(modelID) {
		return nil, fmt.Errorf(
			"anthropic: model %q does not support forced tool choice mode %q",
			modelID,
			req.ToolChoice.Mode,
		)
	}
	var cacheAfterSystem, cacheAfterTools bool
	if req.Cache != nil {
		cacheAfterSystem = req.Cache.AfterSystem
		cacheAfterTools = req.Cache.AfterTools
	}
	tools, canonToProv, provToCanon, err := encodeTools(
		ctx,
		req.Tools,
		cacheAfterTools,
		c.toolExamplesInSchema,
	)
	if err != nil {
		return nil, err
	}
	noArgumentTools := make(map[string]struct{})
	for _, definition := range req.Tools {
		if definition.NoArguments {
			noArgumentTools[definition.Name] = struct{}{}
		}
	}
	msgs, system, err := encodeMessages(req.Messages, canonToProv, cacheAfterSystem)
	if err != nil {
		return nil, err
	}
	enc := &encodedRequest{
		model:           modelID,
		messages:        msgs,
		system:          system,
		tools:           tools,
		outputConfig:    outputConfig,
		provToCanon:     provToCanon,
		noArgumentTools: noArgumentTools,
		opts:            toolExampleOptions(tools),
	}
	if req.ToolChoice != nil {
		tc, err := encodeToolChoice(req.ToolChoice, canonToProv, req.Tools)
		if err != nil {
			return nil, err
		}
		enc.toolChoice = tc
	}
	return enc, nil
}

// completionParams layers completion policy over the canonical encoding: the
// positive max_tokens requirement, the default temperature (omitted for
// models that reject the parameter), and the thinking configuration with its
// budget validation.
func (c *provider) completionParams(ctx context.Context, req *model.Request, enc *encodedRequest) (*sdk.MessageNewParams, error) {
	if err := validateAnthropicTemperature(float64(req.Temperature)); err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	maxTokens := c.effectiveMaxTokens(req.MaxTokens)
	if maxTokens <= 0 {
		return nil, errors.New("anthropic: max_tokens must be positive")
	}
	params := sdk.MessageNewParams{
		MaxTokens:    int64(maxTokens),
		Messages:     enc.messages,
		Model:        enc.model,
		OutputConfig: enc.outputConfig,
		ToolChoice:   enc.toolChoice,
	}
	if len(enc.system) > 0 {
		params.System = enc.system
	}
	if len(enc.tools) > 0 {
		params.Tools = enc.tools
	}
	if t := c.effectiveTemperature(req.Temperature); t > 0 {
		if claudecaps.TemperatureSupported(enc.model) {
			params.Temperature = sdk.Float(t)
		} else {
			traceTemperatureOmitted(ctx, enc.model, t)
		}
	}
	thinking, err := c.thinkingConfig(req, enc.model, maxTokens)
	if err != nil {
		return nil, err
	}
	params.Thinking = thinking
	return &params, nil
}

// thinkingConfig translates the request's thinking choice once for both
// completion and token counting.
func (c *provider) thinkingConfig(req *model.Request, modelID string, maxTokens int) (sdk.ThinkingConfigParamUnion, error) {
	if req.Thinking == nil || !req.Thinking.Enable {
		return sdk.ThinkingConfigParamUnion{}, nil
	}
	if claudecaps.AdaptiveThinkingSupported(modelID) {
		adaptive := sdk.ThinkingConfigAdaptiveParam{
			Display: sdk.ThinkingConfigAdaptiveDisplaySummarized,
		}
		return sdk.ThinkingConfigParamUnion{OfAdaptive: &adaptive}, nil
	}
	if forcesToolUse(req.ToolChoice) {
		return sdk.ThinkingConfigParamUnion{}, errors.New(
			"anthropic: manual thinking cannot be combined with forced tool choice",
		)
	}
	budget := req.Thinking.BudgetTokens
	if budget <= 0 {
		budget = int(c.think)
	}
	if budget <= 0 {
		return sdk.ThinkingConfigParamUnion{}, errors.New(
			"anthropic: thinking budget is required when thinking is enabled",
		)
	}
	if budget < 1024 {
		return sdk.ThinkingConfigParamUnion{}, fmt.Errorf(
			"anthropic: thinking budget %d must be >= 1024",
			budget,
		)
	}
	if maxTokens > 0 && budget >= maxTokens {
		return sdk.ThinkingConfigParamUnion{}, fmt.Errorf(
			"anthropic: thinking budget %d must be less than max_tokens %d",
			budget,
			maxTokens,
		)
	}
	return sdk.ThinkingConfigParamOfEnabled(int64(budget)), nil
}

// validateAnthropicTemperature enforces the Messages API sampling range before
// request construction.
func validateAnthropicTemperature(temperature float64) error {
	if math.IsNaN(temperature) || math.IsInf(temperature, 0) || temperature < 0 || temperature > 1 {
		return errors.New("temperature must be between 0 and 1")
	}
	return nil
}

func (c *provider) effectiveMaxTokens(requested int) int {
	if requested > 0 {
		return requested
	}
	return c.maxTok
}

func (c *provider) effectiveTemperature(requested float32) float64 {
	if requested > 0 {
		return float64(requested)
	}
	return c.temp
}

// toolExampleOptions activates Anthropic's tool-examples beta when
// encodeTools emitted at least one input_examples field. WithHeaderAdd
// appends to any anthropic-beta headers the caller configured on the SDK
// client — the SDK encodes stacked betas as repeated headers — so enabling
// this beta never drops another. The header is required on the direct API,
// recognized by header-compatible gateways (Bedrock Mantle), and ignored by
// Claude-on-Vertex, which delivers input_examples natively with no beta
// activation (live-verified via rawPredict usage.input_tokens, 2026-07-18).
func toolExampleOptions(toolParams []sdk.ToolUnionParam) []option.RequestOption {
	for _, tool := range toolParams {
		if len(tool.OfTool.InputExamples) > 0 {
			return []option.RequestOption{
				option.WithHeaderAdd("anthropic-beta", claudebeta.ToolExamples),
			}
		}
	}
	return nil
}

func encodeMessages(msgs []*model.Message, nameMap map[string]string, cacheAfterSystem bool) ([]sdk.MessageParam, []sdk.TextBlockParam, error) {
	toolUseIDs := tooluseid.NewMapper(msgs)
	conversation := make([]sdk.MessageParam, 0, len(msgs))
	system := make([]sdk.TextBlockParam, 0, len(msgs))

	for _, m := range msgs {
		if m == nil {
			continue
		}
		if m.Role == model.ConversationRoleSystem {
			for _, p := range m.Parts {
				switch v := p.(type) {
				case model.TextPart:
					if v.Text != "" {
						system = append(system, sdk.TextBlockParam{Text: v.Text})
					}
				case model.CitationsPart:
					return nil, nil, errors.New("anthropic: replaying canonical citations is not supported")
				case model.ImagePart:
					_, err := encodeImage(v, m.Role)
					return nil, nil, err
				default:
					return nil, nil, fmt.Errorf("anthropic: unsupported system message part %T", p)
				}
			}
			continue
		}

		blocks := make([]sdk.ContentBlockParamUnion, 0, len(m.Parts))
		for _, part := range m.Parts {
			if v, ok := part.(model.ThinkingPart); ok {
				if m.Role != model.ConversationRoleAssistant {
					return nil, nil, errors.New("anthropic: thinking parts are only supported in assistant messages")
				}
				// Signature without text is valid (thinking display "omitted"
				// on Opus 4.8-class models); text without signature is not
				// replayable to the provider.
				hasSigned := v.Signature != ""
				hasRedacted := len(v.Redacted) > 0
				if hasSigned == hasRedacted || (!hasSigned && v.Text != "") {
					return nil, nil, errors.New("anthropic: thinking part must contain exactly signed content or redacted content")
				}
				if hasSigned {
					blocks = append(blocks, sdk.NewThinkingBlock(v.Signature, v.Text))
				} else {
					blocks = append(blocks, sdk.NewRedactedThinkingBlock(string(v.Redacted)))
				}
				continue
			}
			if v, ok := part.(model.TextPart); ok {
				if v.Text != "" {
					blocks = append(blocks, sdk.NewTextBlock(v.Text))
				}
				continue
			}
			if v, ok := part.(model.ImagePart); ok {
				image, err := encodeImage(v, m.Role)
				if err != nil {
					return nil, nil, err
				}
				blocks = append(blocks, image)
				continue
			}
			if _, ok := part.(model.CitationsPart); ok {
				return nil, nil, errors.New("anthropic: replaying canonical citations is not supported")
			}
			if v, ok := part.(model.ToolUsePart); ok {
				if v.Name == "" {
					return nil, nil, errors.New("anthropic: tool_use part missing name")
				}
				providerName, err := toolname.ProviderName(v.Name, nameMap)
				if err != nil {
					return nil, nil, fmt.Errorf("anthropic: %w", err)
				}
				blocks = append(blocks, sdk.NewToolUseBlock(toolUseIDs.ID(v.ID), v.Input, providerName))
				continue
			}
			if v, ok := part.(model.ToolResultPart); ok {
				result, err := encodeToolResult(v, toolUseIDs.ID(v.ToolUseID))
				if err != nil {
					return nil, nil, err
				}
				blocks = append(blocks, result)
				continue
			}
			return nil, nil, fmt.Errorf("anthropic: unsupported %s message part %T", m.Role, part)
		}
		if len(blocks) == 0 {
			continue
		}
		switch m.Role { //nolint:exhaustive
		case model.ConversationRoleUser:
			conversation = append(conversation, sdk.NewUserMessage(blocks...))
		case model.ConversationRoleAssistant:
			conversation = append(conversation, sdk.NewAssistantMessage(blocks...))
		default:
			return nil, nil, fmt.Errorf("anthropic: unsupported message role %q", m.Role)
		}
	}
	if len(conversation) == 0 {
		return nil, nil, errors.New("anthropic: at least one user/assistant message is required")
	}
	if cacheAfterSystem && len(system) > 0 {
		system[len(system)-1].CacheControl = sdk.NewCacheControlEphemeralParam()
	}
	return conversation, system, nil
}

// encodeImage converts user-supplied image bytes into the base64 source shape
// accepted by Anthropic Messages. Claude does not accept image blocks in
// system or assistant messages, so callers receive an error before any
// provider request is sent.
func encodeImage(image model.ImagePart, role model.ConversationRole) (sdk.ContentBlockParamUnion, error) {
	if role != model.ConversationRoleUser {
		return sdk.ContentBlockParamUnion{}, fmt.Errorf(
			"anthropic: image parts are only supported in user messages (role=%s)",
			role,
		)
	}
	var mediaType sdk.Base64ImageSourceMediaType
	switch image.Format {
	case model.ImageFormatPNG:
		mediaType = sdk.Base64ImageSourceMediaTypeImagePNG
	case model.ImageFormatJPEG:
		mediaType = sdk.Base64ImageSourceMediaTypeImageJPEG
	case model.ImageFormatGIF:
		mediaType = sdk.Base64ImageSourceMediaTypeImageGIF
	case model.ImageFormatWEBP:
		mediaType = sdk.Base64ImageSourceMediaTypeImageWebP
	default:
		return sdk.ContentBlockParamUnion{}, fmt.Errorf("anthropic: unsupported image format %q", image.Format)
	}
	return sdk.NewImageBlock(sdk.Base64ImageSourceParam{
		Data:      base64.StdEncoding.EncodeToString(image.Bytes),
		MediaType: mediaType,
	}), nil
}

func encodeToolResult(v model.ToolResultPart, providerToolUseID string) (sdk.ContentBlockParamUnion, error) {
	var content string
	switch c := v.Content.(type) {
	case nil:
		content = ""
	case string:
		content = c
	case []byte:
		content = string(c)
	default:
		data, err := json.Marshal(c)
		if err != nil {
			return sdk.ContentBlockParamUnion{}, fmt.Errorf("anthropic: encode tool result %q: %w", v.ToolUseID, err)
		}
		content = string(data)
	}
	return sdk.NewToolResultBlock(providerToolUseID, content, v.IsError), nil
}

func encodeTools(
	ctx context.Context,
	defs []*model.ToolDefinition,
	cacheAfterTools, examplesInSchema bool,
) ([]sdk.ToolUnionParam, map[string]string, map[string]string, error) {
	if len(defs) == 0 {
		return nil, nil, nil, nil
	}
	canonToProv, provToCanon, err := toolname.BuildMaps(defs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("anthropic: %w", err)
	}
	toolList := make([]sdk.ToolUnionParam, 0, len(defs))
	for _, def := range defs {
		if def.Description == "" {
			return nil, nil, nil, fmt.Errorf("anthropic: tool %q is missing description", def.Name)
		}
		input, examples, err := anthropicToolInput(ctx, def, examplesInSchema)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("anthropic: tool %q schema: %w", def.Name, err)
		}
		u := sdk.ToolUnionParamOfTool(input, canonToProv[def.Name])
		u.OfTool.Description = sdk.String(def.Description)
		u.OfTool.InputExamples = examples
		toolList = append(toolList, u)
	}
	if cacheAfterTools {
		toolList[len(toolList)-1].OfTool.CacheControl = sdk.NewCacheControlEphemeralParam()
	}
	return toolList, canonToProv, provToCanon, nil
}

// anthropicToolInput projects one tool definition into the representation
// selected when the provider was constructed. Direct Anthropic pairs the plain
// schema with input_examples. Messages-compatible gateways such as Bedrock
// retain the authored root example inside the schema instead.
func anthropicToolInput(
	ctx context.Context,
	def *model.ToolDefinition,
	exampleInSchema bool,
) (sdk.ToolInputSchemaParam, []map[string]any, error) {
	input := def.Input.Contract()
	example := input.ExampleJSON
	if example == nil || exampleInSchema {
		schema, err := toolInputSchema(ctx, input.Schema)
		return schema, nil, err
	}
	if input.SchemaWithoutRootExample == nil {
		return sdk.ToolInputSchemaParam{}, nil, errors.New("example JSON requires schema without root example")
	}
	schema, err := toolInputSchema(ctx, input.SchemaWithoutRootExample)
	if err != nil {
		return sdk.ToolInputSchemaParam{}, nil, err
	}
	exampleInput, err := toolExampleInput(example)
	if err != nil {
		return sdk.ToolInputSchemaParam{}, nil, err
	}
	return schema, []map[string]any{exampleInput}, nil
}

func toolInputSchema(_ context.Context, schema rawjson.Message) (sdk.ToolInputSchemaParam, error) {
	raw := bytes.TrimSpace(schema)
	if len(raw) == 0 {
		return sdk.ToolInputSchemaParam{}, nil
	}
	m, err := decodeToolJSONObject(raw)
	if err != nil {
		return sdk.ToolInputSchemaParam{}, err
	}
	return sdk.ToolInputSchemaParam{
		ExtraFields: m,
	}, nil
}

func toolExampleInput(raw rawjson.Message) (map[string]any, error) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return nil, nil
	}
	return decodeToolJSONObject(data)
}

// decodeToolJSONObject converts a canonical raw tool schema or example to the
// top-level map required by the Anthropic SDK. Raw field values preserve the
// canonical JSON because the SDK's document encoder treats json.Number as a
// string.
func decodeToolJSONObject(data []byte) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("JSON value must be an object")
	}
	object := make(map[string]any, len(fields))
	for name, value := range fields {
		object[name] = value
	}
	return object, nil
}

// forcesToolUse reports whether Anthropic will require a tool-use response.
func forcesToolUse(choice *model.ToolChoice) bool {
	return choice != nil && (choice.Mode == model.ToolChoiceModeAny || choice.Mode == model.ToolChoiceModeTool)
}

func encodeToolChoice(choice *model.ToolChoice, canonToProv map[string]string, defs []*model.ToolDefinition) (sdk.ToolChoiceUnionParam, error) {
	if choice == nil {
		return sdk.ToolChoiceUnionParam{}, nil
	}
	switch choice.Mode {
	case "", model.ToolChoiceModeAuto:
		return sdk.ToolChoiceUnionParam{}, nil
	case model.ToolChoiceModeNone:
		none := sdk.NewToolChoiceNoneParam()
		return sdk.ToolChoiceUnionParam{OfNone: &none}, nil
	case model.ToolChoiceModeAny:
		return sdk.ToolChoiceUnionParam{
			OfAny: &sdk.ToolChoiceAnyParam{},
		}, nil
	case model.ToolChoiceModeTool:
		if choice.Name == "" {
			return sdk.ToolChoiceUnionParam{}, fmt.Errorf("anthropic: tool choice mode %q requires a tool name", choice.Mode)
		}
		if !hasToolDefinition(defs, choice.Name) {
			return sdk.ToolChoiceUnionParam{}, fmt.Errorf("anthropic: tool choice name %q does not match any tool", choice.Name)
		}
		sanitized, ok := canonToProv[choice.Name]
		if !ok || sanitized == "" {
			return sdk.ToolChoiceUnionParam{}, fmt.Errorf("anthropic: tool choice name %q does not match any tool", choice.Name)
		}
		tool := sdk.ToolChoiceParamOfTool(sanitized)
		return tool, nil
	default:
		return sdk.ToolChoiceUnionParam{}, fmt.Errorf("anthropic: unsupported tool choice mode %q", choice.Mode)
	}
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

// wrapAnthropicError classifies an error surfaced by the Anthropic Messages
// API into the goa-ai provider error contract via model.ClassifyHTTPStatus.
// Real SDK failures carry the HTTP status on *sdk.Error (the SDK always
// returns a pointer); this extracts that status and a panic-safe message and
// hands both to the shared classifier so the same status-to-kind table backs
// every Anthropic-hosted adapter, including features/model/vertex's
// Claude-on-Vertex constructor, which builds this client directly against
// the SDK's Vertex transport and relies on this function for its error
// classification.
//
// Context cancellation and deadline errors pass through unwrapped: they are
// consumer-side flow control, not provider failures, and must not be
// classified. (io.EOF never reaches this function; the streamer surfaces
// normal termination as a nil stream error and emits io.EOF itself.)
//
// Non-SDK errors (including bare model.ErrRateLimited sentinels used by
// tests and any caller that pre-classifies) are classified with status 0
// (kind unknown); the cause is still preserved as the Unwrap target, so
// errors.Is(result, model.ErrRateLimited) keeps working through the chain.
func wrapAnthropicError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	status := 0
	message := ""
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		status = apiErr.StatusCode
		message = anthropicErrorMessage(apiErr)
	} else {
		message = err.Error()
	}
	return model.ClassifyHTTPStatus(anthropicProviderName, operation, status, message, err)
}

// anthropicErrorMessage safely renders an *sdk.Error's message.
//
// (*sdk.Error).Error() unconditionally dereferences both Request and
// Response (see the SDK's internal/apierror/apierror.go), which the SDK
// always populates when it constructs the error from a live HTTP round trip
// but which are nil on any error built without one — including
// hand-constructed test doubles. Calling apiErr.Error() in that case panics
// with a nil pointer dereference instead of returning a string, so this
// falls back to a status-only message whenever either field is missing.
func anthropicErrorMessage(apiErr *sdk.Error) string {
	if apiErr.Request == nil || apiErr.Response == nil {
		return fmt.Sprintf("anthropic api error: status %d", apiErr.StatusCode)
	}
	return apiErr.Error()
}

func translateResponse(msg *sdk.Message, nameMap map[string]string) (*model.Response, error) {
	if msg == nil {
		return nil, outputvalidation.New(
			model.OutputValidationResponseShape,
			errors.New("anthropic: response message is nil"),
		)
	}
	resp := &model.Response{}
	assistant := model.Message{Role: model.ConversationRoleAssistant}
	thinkingIndex := 0
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if len(block.Citations) == 0 {
				assistant.Parts = append(assistant.Parts, model.TextPart{Text: block.Text})
				continue
			}
			citations, err := translateCitations(block.Citations)
			if err != nil {
				return nil, outputvalidation.New(model.OutputValidationResponseShape, err)
			}
			assistant.Parts = append(assistant.Parts, model.CitationsPart{
				Text:      block.Text,
				Citations: citations,
			})
		case "thinking":
			if block.Signature == "" {
				return nil, outputvalidation.New(
					model.OutputValidationResponseShape,
					errors.New("anthropic: response thinking block requires signature"),
				)
			}
			assistant.Parts = append(assistant.Parts, model.ThinkingPart{
				Text:      block.Thinking,
				Signature: block.Signature,
				Index:     thinkingIndex,
				Final:     true,
			})
			thinkingIndex++
		case "redacted_thinking":
			if block.Data == "" {
				return nil, outputvalidation.New(
					model.OutputValidationResponseShape,
					errors.New("anthropic: response redacted thinking block requires data"),
				)
			}
			assistant.Parts = append(assistant.Parts, model.ThinkingPart{
				Redacted: []byte(block.Data),
				Index:    thinkingIndex,
				Final:    true,
			})
			thinkingIndex++
		case "tool_use":
			if block.ID == "" {
				return nil, outputvalidation.New(
					model.OutputValidationToolIdentity,
					errors.New("anthropic: response tool use block missing ID"),
				)
			}
			if block.Name == "" {
				return nil, outputvalidation.New(
					model.OutputValidationToolIdentity,
					fmt.Errorf("anthropic: response tool use block %q missing name", block.ID),
				)
			}
			raw := block.Name
			name, ok := nameMap[raw]
			if !ok {
				return nil, outputvalidation.New(
					model.OutputValidationToolIdentity,
					fmt.Errorf(
						"anthropic: translate response tool use: %w",
						model.NewUnadvertisedToolNameError(raw),
					),
				)
			}
			payload := rawjson.Message(block.Input)
			assistant.Parts = append(assistant.Parts, model.ToolUsePart{
				Name:  string(tools.Ident(name)),
				Input: payload,
				ID:    block.ID,
			})
		default:
			return nil, outputvalidation.New(
				model.OutputValidationResponseShape,
				fmt.Errorf("anthropic: unsupported response content block %q", block.Type),
			)
		}
	}
	if len(assistant.Parts) > 0 {
		resp.Content = append(resp.Content, assistant)
	}
	if u := msg.Usage; u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheReadInputTokens != 0 || u.CacheCreationInputTokens != 0 {
		resp.Usage = model.TokenUsage{
			InputTokens:      int(u.InputTokens),
			OutputTokens:     int(u.OutputTokens),
			TotalTokens:      int(u.InputTokens + u.OutputTokens),
			CacheReadTokens:  int(u.CacheReadInputTokens),
			CacheWriteTokens: int(u.CacheCreationInputTokens),
		}
	}
	resp.StopReason = string(msg.StopReason)
	if resp.StopReason == "" {
		return nil, outputvalidation.New(
			model.OutputValidationResponseShape,
			errors.New("anthropic: response is missing its stop reason"),
		)
	}
	resp.OutputLimited = anthropicOutputLimited(resp.StopReason)
	return resp, nil
}

// anthropicOutputLimited identifies the Anthropic stop reason emitted when a
// message consumes its configured generated-token budget.
func anthropicOutputLimited(reason string) bool {
	return reason == string(sdk.StopReasonMaxTokens)
}

// translateAnthropicUsage extracts provider usage and resolved model identity
// before content translation so malformed content cannot erase valid billing
// evidence.
func translateAnthropicUsage(msg *sdk.Message, modelID string, modelClass model.ModelClass) model.TokenUsage {
	if msg == nil {
		return model.TokenUsage{
			Model:      modelID,
			ModelClass: modelClass,
		}
	}
	usage := msg.Usage
	return model.TokenUsage{
		Model:            modelID,
		ModelClass:       modelClass,
		InputTokens:      int(usage.InputTokens),
		OutputTokens:     int(usage.OutputTokens),
		TotalTokens:      int(usage.InputTokens + usage.OutputTokens),
		CacheReadTokens:  int(usage.CacheReadInputTokens),
		CacheWriteTokens: int(usage.CacheCreationInputTokens),
	}
}

// translateCitations preserves every Anthropic text citation in the canonical
// location model or rejects citation kinds that cannot be represented.
func translateCitations(input []sdk.TextCitationUnion) ([]model.Citation, error) {
	out := make([]model.Citation, 0, len(input))
	for index, citation := range input {
		translated := model.Citation{
			Title: citation.DocumentTitle,
		}
		if citation.CitedText != "" {
			translated.SourceContent = []string{citation.CitedText}
		}
		switch citation.Type {
		case "char_location":
			translated.Source = citation.FileID
			translated.Location.DocumentChar = &model.DocumentCharLocation{
				DocumentIndex: int(citation.DocumentIndex),
				Start:         int(citation.StartCharIndex),
				End:           int(citation.EndCharIndex),
			}
		case "page_location":
			translated.Source = citation.FileID
			translated.Location.DocumentPage = &model.DocumentPageLocation{
				DocumentIndex: int(citation.DocumentIndex),
				Start:         int(citation.StartPageNumber),
				End:           int(citation.EndPageNumber),
			}
		case "content_block_location":
			translated.Source = citation.FileID
			translated.Location.DocumentChunk = &model.DocumentChunkLocation{
				DocumentIndex: int(citation.DocumentIndex),
				Start:         int(citation.StartBlockIndex),
				End:           int(citation.EndBlockIndex),
			}
		case "web_search_result_location":
			translated.Title = citation.Title
			translated.Source = citation.URL
		case "search_result_location":
			translated.Source = citation.Source
		default:
			return nil, fmt.Errorf("anthropic: unsupported citation type %q at index %d", citation.Type, index)
		}
		out = append(out, translated)
	}
	return out, nil
}
