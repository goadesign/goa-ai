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
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

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
	params, provToCanon, err := c.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	msg, err := c.msg.New(ctx, *params)
	if err != nil {
		return nil, wrapAnthropicError("complete", err)
	}
	return translateResponse(msg, provToCanon)
}

// Stream invokes Messages.NewStreaming and adapts incremental events into
// model.Chunks so planners can surface partial responses.
func (c *Client) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	params, provToCanon, err := c.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	stream := c.msg.NewStreaming(ctx, *params)
	if err := stream.Err(); err != nil {
		return nil, wrapAnthropicError("stream", err)
	}
	return newAnthropicStreamer(ctx, stream, provToCanon), nil
}

func (c *Client) prepareRequest(ctx context.Context, req *model.Request) (*sdk.MessageNewParams, map[string]string, error) {
	if len(req.Messages) == 0 {
		return nil, nil, errors.New("anthropic: messages are required")
	}
	if req.StructuredOutput != nil {
		return nil, nil, fmt.Errorf(
			"anthropic messages do not support structured output: %w",
			model.ErrStructuredOutputUnsupported,
		)
	}
	modelID := c.resolveModelID(req)
	if modelID == "" {
		return nil, nil, errors.New("anthropic: model identifier is required")
	}
	tools, canonToProv, provToCanon, err := encodeTools(ctx, req.Tools)
	if err != nil {
		return nil, nil, err
	}
	msgs, system, err := encodeMessages(req.Messages, canonToProv)
	if err != nil {
		return nil, nil, err
	}
	maxTokens := c.effectiveMaxTokens(req.MaxTokens)
	if maxTokens <= 0 {
		return nil, nil, errors.New("anthropic: max_tokens must be positive")
	}
	params := sdk.MessageNewParams{
		MaxTokens: int64(maxTokens),
		Messages:  msgs,
		Model:     modelID,
	}
	if len(system) > 0 {
		params.System = system
	}
	if len(tools) > 0 {
		params.Tools = tools
	}
	if t := c.effectiveTemperature(req.Temperature); t > 0 {
		params.Temperature = sdk.Float(t)
	}
	if req.Thinking != nil && req.Thinking.Enable && !forcesToolUse(req.ToolChoice) {
		budget := req.Thinking.BudgetTokens
		if budget <= 0 {
			budget = int(c.think)
		}
		if budget <= 0 {
			return nil, nil, errors.New("anthropic: thinking budget is required when thinking is enabled")
		}
		if budget < 1024 {
			return nil, nil, fmt.Errorf("anthropic: thinking budget %d must be >= 1024", budget)
		}
		if int64(budget) >= int64(maxTokens) {
			return nil, nil, fmt.Errorf("anthropic: thinking budget %d must be less than max_tokens %d", budget, maxTokens)
		}
		params.Thinking = sdk.ThinkingConfigParamOfEnabled(int64(budget))
	}
	if req.ToolChoice != nil {
		tc, err := encodeToolChoice(req.ToolChoice, canonToProv, req.Tools)
		if err != nil {
			return nil, nil, err
		}
		params.ToolChoice = tc
	}
	return &params, provToCanon, nil
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

func encodeMessages(msgs []*model.Message, nameMap map[string]string) ([]sdk.MessageParam, []sdk.TextBlockParam, error) {
	conversation := make([]sdk.MessageParam, 0, len(msgs))
	system := make([]sdk.TextBlockParam, 0, len(msgs))

	for _, m := range msgs {
		if m == nil {
			continue
		}
		if m.Role == model.ConversationRoleSystem {
			for _, p := range m.Parts {
				if v, ok := p.(model.TextPart); ok && v.Text != "" {
					system = append(system, sdk.TextBlockParam{Text: v.Text})
				}
			}
			continue
		}

		blocks := make([]sdk.ContentBlockParamUnion, 0, len(m.Parts))
		for _, part := range m.Parts {
			if v, ok := part.(model.TextPart); ok {
				if v.Text != "" {
					blocks = append(blocks, sdk.NewTextBlock(v.Text))
				}
				continue
			}
			if v, ok := part.(model.ToolUsePart); ok {
				if v.Name == "" {
					return nil, nil, errors.New("anthropic: tool_use part missing name")
				}
				if sanitized, ok := nameMap[v.Name]; ok && sanitized != "" {
					blocks = append(blocks, sdk.NewToolUseBlock(v.ID, v.Input, sanitized))
					continue
				}
				unavailable := tools.ToolUnavailable.String()
				sanitized, ok := nameMap[unavailable]
				if !ok || sanitized == "" {
					return nil, nil, fmt.Errorf(
						"anthropic: tool_use in messages references %q which is not in the current tool configuration and tool_unavailable is not available",
						v.Name,
					)
				}
				blocks = append(blocks, sdk.NewToolUseBlock(v.ID, map[string]any{
					"requested_tool":    v.Name,
					"requested_payload": v.Input,
				}, sanitized))
				continue
			}
			if v, ok := part.(model.ToolResultPart); ok {
				blocks = append(blocks, encodeToolResult(v))
				continue
			}
			// Thinking and cache checkpoint parts are provider-specific and are
			// not re-encoded for Anthropic here.
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
	return conversation, system, nil
}

func encodeToolResult(v model.ToolResultPart) sdk.ContentBlockParamUnion {
	var content string
	switch c := v.Content.(type) {
	case nil:
		content = ""
	case string:
		content = c
	case []byte:
		content = string(c)
	default:
		if data, err := json.Marshal(c); err == nil {
			content = string(data)
		}
	}
	return sdk.NewToolResultBlock(v.ToolUseID, content, v.IsError)
}

func encodeTools(ctx context.Context, defs []*model.ToolDefinition) ([]sdk.ToolUnionParam, map[string]string, map[string]string, error) {
	if len(defs) == 0 {
		return nil, nil, nil, nil
	}
	toolList := make([]sdk.ToolUnionParam, 0, len(defs))
	canonToSan := make(map[string]string, len(defs))
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
				"anthropic: tool name %q sanitizes to %q which collides with %q",
				canonical, sanitized, prev,
			)
		}
		sanToCanon[sanitized] = canonical
		canonToSan[canonical] = sanitized
		if def.Description == "" {
			return nil, nil, nil, fmt.Errorf("anthropic: tool %q is missing description", canonical)
		}
		input, examples, err := anthropicToolInput(ctx, def)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("anthropic: tool %q schema: %w", canonical, err)
		}
		u := sdk.ToolUnionParamOfTool(input, sanitized)
		if u.OfTool != nil {
			u.OfTool.Description = sdk.String(def.Description)
			u.OfTool.InputExamples = examples
		}
		toolList = append(toolList, u)
	}
	if len(toolList) == 0 {
		return nil, nil, nil, nil
	}
	return toolList, canonToSan, sanToCanon, nil
}

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
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
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
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
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

// sanitizeToolName maps a canonical tool identifier to characters allowed by
// Anthropic tool naming constraints by replacing any disallowed rune with '_'.
// Canonical tool identifiers follow the pattern "toolset.tool". To keep tool
// names concise and avoid redundant prefixes in provider-facing configs, this
// helper derives the base name from the segment after the final '.' and, when
// present, strips a "<toolset_suffix>_" prefix.
func sanitizeToolName(in string) string {
	if in == "" {
		return in
	}
	base := in
	if idx := strings.LastIndex(in, "."); idx >= 0 && idx+1 < len(in) {
		base = in[idx+1:]
		if idx > 0 {
			if lastDot := strings.LastIndex(in[:idx], "."); lastDot >= 0 && lastDot+1 < idx {
				toolsetSuffix := in[lastDot+1 : idx]
				prefix := toolsetSuffix + "_"
				if strings.HasPrefix(base, prefix) && len(base) > len(prefix) {
					base = base[len(prefix):]
				}
			}
		}
	}
	if isProviderSafeToolName(base) {
		return base
	}
	out := make([]rune, 0, len(base))
	for _, r := range base {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func isProviderSafeToolName(name string) bool {
	if name == "" {
		return false
	}
	if len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
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
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			resp.Content = append(resp.Content, model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: block.Text}},
			})
		case "tool_use":
			payload := rawjson.Message(block.Input)
			name := ""
			if block.Name != "" {
				raw := block.Name
				// When the model hallucinates a tool name that was not advertised in
				// this request, the reverse map will not contain it. Surface the tool
				// call as-is and let the runtime return an "unknown tool" error result.
				if canonical, ok := nameMap[raw]; ok {
					name = canonical
				} else {
					name = raw
				}
			}
			resp.ToolCalls = append(resp.ToolCalls, model.ToolCall{
				Name:    tools.Ident(name),
				Payload: payload,
				ID:      block.ID,
			})
		}
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
	return resp, nil
}
