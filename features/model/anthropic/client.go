// Package anthropic provides a model.Client implementation backed by the
// Anthropic Claude Messages API. It translates goa-ai requests into
// anthropic.Message calls using github.com/anthropics/anthropic-sdk-go and maps
// responses (text, tools, thinking, usage) back into the generic planner
// structures.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"goa.design/goa-ai/features/model/internal/claudebeta"
	"goa.design/goa-ai/features/model/internal/claudecaps"
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
		model       string
		messages    []sdk.MessageParam
		system      []sdk.TextBlockParam
		tools       []sdk.ToolUnionParam
		toolChoice  sdk.ToolChoiceUnionParam
		provToCanon map[string]string
		opts        []option.RequestOption
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
	}

	// Client implements model.Client on top of Anthropic Claude Messages.
	Client struct {
		msg          MessagesClient
		defaultModel string
		highModel    string
		smallModel   string
		maxTok       int
		temp         float64
		think        int64
	}
)

var (
	_ model.Client       = (*Client)(nil)
	_ model.TokenCounter = (*Client)(nil)
)

// New builds an Anthropic-backed model client from the provided Anthropic
// Messages client and configuration options.
func New(msg MessagesClient, opts Options) (*Client, error) {
	if msg == nil {
		return nil, errors.New("anthropic client is required")
	}
	if opts.DefaultModel == "" {
		return nil, errors.New("default model identifier is required")
	}
	maxTokens := opts.MaxTokens
	thinkBudget := opts.ThinkingBudget

	c := &Client{
		msg:          msg,
		defaultModel: opts.DefaultModel,
		highModel:    opts.HighModel,
		smallModel:   opts.SmallModel,
		maxTok:       maxTokens,
		temp:         opts.Temperature,
		think:        thinkBudget,
	}
	return c, nil
}

// NewFromAPIKey constructs a client using the default Anthropic HTTP client.
// It reads ANTHROPIC_API_KEY and related defaults from the environment via
// sdk.DefaultClientOptions.
func NewFromAPIKey(apiKey, defaultModel string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("api key is required")
	}
	ac := sdk.NewClient(option.WithAPIKey(apiKey))
	return New(&ac.Messages, Options{DefaultModel: defaultModel})
}

// Complete issues a non-streaming Messages.New request and translates the
// response into planner-friendly structures (assistant messages + tool calls).
func (c *Client) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	enc, err := c.encodeRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	params, err := c.completionParams(ctx, req, enc)
	if err != nil {
		return nil, err
	}
	msg, err := c.msg.New(ctx, *params, enc.opts...)
	if err != nil {
		return nil, wrapAnthropicError("complete", err)
	}
	return translateResponse(msg, enc.provToCanon)
}

// CountTokens asks Anthropic to count the exact provider input built from req.
// It consumes the canonical encoding directly — counting carries no completion
// policy, so a client without a default MaxTokens counts exactly what an
// explicit-cap completion would send. Replayed thinking is excluded per the
// model.TokenCounter contract.
func (c *Client) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	enc, err := c.encodeRequest(ctx, model.CountingRequest(req))
	if err != nil {
		return model.TokenCount{}, err
	}
	countParams := sdk.MessageCountTokensParams{
		Messages:   enc.messages,
		Model:      enc.model,
		ToolChoice: enc.toolChoice,
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
func (c *Client) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	enc, err := c.encodeRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	params, err := c.completionParams(ctx, req, enc)
	if err != nil {
		return nil, err
	}
	stream := c.msg.NewStreaming(ctx, *params, enc.opts...)
	if err := stream.Err(); err != nil {
		return nil, wrapAnthropicError("stream", err)
	}
	return newAnthropicStreamer(ctx, stream, enc.provToCanon), nil
}

// encodeRequest builds the canonical Anthropic encoding of req: resolved
// model, transcript, tools (with cache markers and per-request name maps),
// tool choice, and the request options the encoded contracts require. It
// validates only what every call path needs; completion-only requirements
// live in completionParams.
func (c *Client) encodeRequest(ctx context.Context, req *model.Request) (*encodedRequest, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("anthropic: messages are required")
	}
	if req.StructuredOutput != nil {
		return nil, fmt.Errorf(
			"anthropic messages do not support structured output: %w",
			model.ErrStructuredOutputUnsupported,
		)
	}
	modelID := c.resolveModelID(req)
	if modelID == "" {
		return nil, errors.New("anthropic: model identifier is required")
	}
	var cacheAfterSystem, cacheAfterTools bool
	if req.Cache != nil {
		cacheAfterSystem = req.Cache.AfterSystem
		cacheAfterTools = req.Cache.AfterTools
	}
	tools, canonToProv, provToCanon, err := encodeTools(ctx, req.Tools, cacheAfterTools)
	if err != nil {
		return nil, err
	}
	msgs, system, err := encodeMessages(req.Messages, canonToProv, cacheAfterSystem)
	if err != nil {
		return nil, err
	}
	enc := &encodedRequest{
		model:       modelID,
		messages:    msgs,
		system:      system,
		tools:       tools,
		provToCanon: provToCanon,
		opts:        toolExampleOptions(tools),
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
func (c *Client) completionParams(ctx context.Context, req *model.Request, enc *encodedRequest) (*sdk.MessageNewParams, error) {
	maxTokens := c.effectiveMaxTokens(req.MaxTokens)
	if maxTokens <= 0 {
		return nil, errors.New("anthropic: max_tokens must be positive")
	}
	params := sdk.MessageNewParams{
		MaxTokens:  int64(maxTokens),
		Messages:   enc.messages,
		Model:      enc.model,
		ToolChoice: enc.toolChoice,
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
	if req.Thinking != nil && req.Thinking.Enable && !forcesToolUse(req.ToolChoice) {
		budget := req.Thinking.BudgetTokens
		if budget <= 0 {
			budget = int(c.think)
		}
		if budget <= 0 {
			return nil, errors.New("anthropic: thinking budget is required when thinking is enabled")
		}
		if budget < 1024 {
			return nil, fmt.Errorf("anthropic: thinking budget %d must be >= 1024", budget)
		}
		if int64(budget) >= int64(maxTokens) {
			return nil, fmt.Errorf("anthropic: thinking budget %d must be less than max_tokens %d", budget, maxTokens)
		}
		params.Thinking = sdk.ThinkingConfigParamOfEnabled(int64(budget))
	}
	return &params, nil
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

func (c *Client) effectiveMaxTokens(requested int) int {
	if requested > 0 {
		return requested
	}
	return c.maxTok
}

func (c *Client) effectiveTemperature(requested float32) float64 {
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
				hasPlaintext := v.Text != "" || v.Signature != ""
				hasRedacted := len(v.Redacted) > 0
				if hasPlaintext == hasRedacted || (v.Text == "") != (v.Signature == "") {
					return nil, nil, errors.New("anthropic: thinking part must contain exactly signed plaintext or redacted content")
				}
				if hasPlaintext {
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
			if _, ok := part.(model.CitationsPart); ok {
				return nil, nil, errors.New("anthropic: replaying canonical citations is not supported")
			}
			if v, ok := part.(model.ToolUsePart); ok {
				if v.Name == "" {
					return nil, nil, errors.New("anthropic: tool_use part missing name")
				}
				if sanitized, ok := nameMap[v.Name]; ok && sanitized != "" {
					blocks = append(blocks, sdk.NewToolUseBlock(v.ID, v.Input, sanitized))
					continue
				}
				for canonical, provider := range nameMap {
					if provider == v.Name {
						return nil, nil, fmt.Errorf(
							"anthropic: historical provider tool name %q collides with current tool %q",
							v.Name,
							canonical,
						)
					}
				}
				blocks = append(blocks, sdk.NewToolUseBlock(v.ID, v.Input, v.Name))
				continue
			}
			if v, ok := part.(model.ToolResultPart); ok {
				result, err := encodeToolResult(v)
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

func encodeToolResult(v model.ToolResultPart) (sdk.ContentBlockParamUnion, error) {
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
	return sdk.NewToolResultBlock(v.ToolUseID, content, v.IsError), nil
}

func encodeTools(ctx context.Context, defs []*model.ToolDefinition, cacheAfterTools bool) ([]sdk.ToolUnionParam, map[string]string, map[string]string, error) {
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
		input, examples, err := anthropicToolInput(ctx, def)
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

// anthropicToolInput projects one tool definition into the SDK schema and
// optional provider-native examples: a tool with an authored root example
// pairs the schema without that example with input_examples; otherwise the
// annotated schema travels alone.
func anthropicToolInput(ctx context.Context, def *model.ToolDefinition) (sdk.ToolInputSchemaParam, []map[string]any, error) {
	input := def.Input
	example := input.ExampleJSON()
	if example == nil {
		schema, err := toolInputSchema(ctx, input.JSONSchema())
		return schema, nil, err
	}
	if input.SchemaWithoutRootExample() == nil {
		return sdk.ToolInputSchemaParam{}, nil, errors.New("example JSON requires schema without root example")
	}
	schema, err := toolInputSchema(ctx, input.SchemaWithoutRootExample())
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
// SDK document shape without rounding JSON numbers.
func decodeToolJSONObject(data []byte) (map[string]any, error) {
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON object")
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("JSON value must be an object")
	}
	return object, nil
}

// forcesToolUse reports whether Anthropic will treat tool_choice as requiring a
// tool-use response. The Messages API rejects thinking for these requests, so
// forced tool-use takes precedence during request encoding.
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
	return model.ClassifyHTTPStatus("anthropic", operation, status, message, err)
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
		return nil, errors.New("anthropic: response message is nil")
	}
	resp := &model.Response{}
	assistant := model.Message{Role: model.ConversationRoleAssistant}
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if len(block.Citations) == 0 {
				assistant.Parts = append(assistant.Parts, model.TextPart{Text: block.Text})
				continue
			}
			citations, err := translateCitations(block.Citations)
			if err != nil {
				return nil, err
			}
			assistant.Parts = append(assistant.Parts, model.CitationsPart{
				Text:      block.Text,
				Citations: citations,
			})
		case "thinking":
			if block.Thinking == "" || block.Signature == "" {
				return nil, errors.New("anthropic: response thinking block requires plaintext and signature")
			}
			assistant.Parts = append(assistant.Parts, model.ThinkingPart{
				Text:      block.Thinking,
				Signature: block.Signature,
				Final:     true,
			})
		case "redacted_thinking":
			if block.Data == "" {
				return nil, errors.New("anthropic: response redacted thinking block requires data")
			}
			assistant.Parts = append(assistant.Parts, model.ThinkingPart{
				Redacted: []byte(block.Data),
				Final:    true,
			})
		case "tool_use":
			if block.ID == "" {
				return nil, errors.New("anthropic: response tool use block missing ID")
			}
			if block.Name == "" {
				return nil, fmt.Errorf("anthropic: response tool use block %q missing name", block.ID)
			}
			payload := rawjson.Message(block.Input)
			raw := block.Name
			name := raw
			// When the model hallucinates a tool name that was not advertised in
			// this request, the reverse map will not contain it. Surface the tool
			// call as-is and let the runtime return an "unknown tool" error result.
			if canonical, ok := nameMap[raw]; ok {
				name = canonical
			}
			assistant.Parts = append(assistant.Parts, model.ToolUsePart{
				Name:  string(tools.Ident(name)),
				Input: payload,
				ID:    block.ID,
			})
		default:
			return nil, fmt.Errorf("anthropic: unsupported response content block %q", block.Type)
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
		return nil, errors.New("anthropic: response is missing its stop reason")
	}
	if err := model.ValidateResponse(resp); err != nil {
		return nil, fmt.Errorf("anthropic: invalid response: %w", err)
	}
	return resp, nil
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
