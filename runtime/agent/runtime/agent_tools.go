// Package runtime provides the goa-ai runtime implementation.
//
// This file contains agent-as-tool support: a toolset registration that executes a
// nested agent run and adapts its canonical run output into a parent tool_result.
//
// Contract highlights:
//   - The nested run context always carries the canonical JSON tool payload as
//     RunContext.ToolArgs for provider planners to decode once and render
//     method-specific prompts.
//   - Consumer-side prompt rendering (PromptSpecs/Templates/Texts) is optional and
//     must be payload-only: it cannot depend on provider-only server context.
//   - When no consumer-side prompt content is configured, the runtime uses the
//     canonical tool payload to construct the nested user message deterministically.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"text/template"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// AgentToolOption configures per-tool content for agent-as-tool registrations.
	// Options are applied to AgentToolConfig before constructing the registration.
	AgentToolOption func(*AgentToolConfig)

	// PromptBuilder builds a user message for a tool call from its payload when
	// no explicit text or template is configured.
	PromptBuilder func(id tools.Ident, payload any) string

	// AgentToolValidator validates a nested agent-tool call after payload decoding
	// and before the child run is started.
	AgentToolValidator func(ctx context.Context, input *AgentToolValidationInput) *AgentToolValidationError

	// AgentToolContent configures the optional consumer-side rendering of the
	// nested agent's initial user message.
	//
	// In most systems the provider planner owns prompt rendering and server-side
	// context injection. In that model consumers should leave AgentToolContent
	// empty and rely on the runtime default: the nested user message is the
	// canonical JSON tool payload bytes.
	//
	// When consumer-side rendering is configured, it must be payload-only: it
	// cannot depend on provider-only server context.
	AgentToolContent struct {
		// Templates maps tool IDs (globally unique) to compiled templates used to render
		// the tool-specific user message from the tool payload. Templates MUST be
		// provided for all tools in this toolset and are compiled with
		// template.Option("missingkey=error").
		Templates map[tools.Ident]*template.Template
		// Texts maps tool IDs (globally unique) to a pure text user message. When a
		// template for a tool is not provided, the runtime uses the corresponding
		// text if present. Exactly one of Templates[id] or Texts[id] should be set
		// per tool. Callers are responsible for ensuring full coverage across tools.
		Texts map[tools.Ident]string
		// PromptSpecs maps tool IDs to prompt registry IDs. When configured for a
		// tool, runtime resolves and renders this prompt first and uses the rendered
		// text as the nested agent user message.
		//
		// Rendering contract: the prompt template root data is the canonical payload
		// JSON object produced by the tool payload codec (schema keys, e.g.
		// snake_case). Templates must reference schema field names, not Go struct
		// field names.
		PromptSpecs map[tools.Ident]prompt.Ident
		// Prompt builds a user message when no PromptSpec, template, or text is
		// configured for a tool.
		Prompt PromptBuilder
	}

	// AgentToolConfig configures how an agent-tool executes.
	//
	// AgentID identifies the nested agent to execute. SystemPrompts optionally
	// maps tool IDs (globally unique simple names) to system prompts that will
	// be prepended to the nested agent messages for that tool.
	AgentToolConfig struct {
		// AgentID is the fully qualified identifier of the nested agent.
		AgentID agent.Ident
		// Route provides the routing metadata used to start the nested agent as a
		// child workflow. Route must be set; agent-as-tool execution does not fall
		// back to local agent registration.
		Route AgentRoute
		// PlanActivityName is the fully-qualified plan activity name for the nested agent.
		PlanActivityName string
		// ResumeActivityName is the fully-qualified resume activity name for the nested agent.
		ResumeActivityName string
		// ExecuteToolActivity is the fully-qualified execute_tool activity name for the nested agent.
		ExecuteToolActivity string
		// SystemPrompt, when non-empty, is prepended as a system message for all tools.
		SystemPrompt string
		// AgentToolContent configures optional consumer-side user message rendering.
		// It is embedded so existing field selectors (cfg.Texts, cfg.PromptSpecs, …)
		// remain valid, but callers are encouraged to treat it as an advanced knob.
		AgentToolContent
		// PreChildValidator optionally validates the decoded payload and current
		// transcript before starting the nested child run. Validation errors are
		// surfaced as tool-scoped retry guidance instead of workflow failures.
		PreChildValidator AgentToolValidator
		// Name optionally sets the toolset registration name (qualified toolset id).
		Name string
		// Description optionally describes the toolset.
		Description string
		// TaskQueue optionally sets the task queue for this toolset's activities.
		TaskQueue string
		// Aliases maps public tool identifiers to canonical provider tool identifiers.
		// This allows consumers to expose tools under a different namespace without
		// duplicating specs or templates. When present, message rendering and provider
		// routing use the canonical name while the public name is preserved in parent
		// stream events.
		Aliases map[tools.Ident]tools.Ident
	}

	// AgentToolValidationInput captures the data available to PreChildValidator.
	AgentToolValidationInput struct {
		Call      *planner.ToolRequest
		Payload   any
		Messages  []*model.Message
		ParentRun *run.Context
	}

	// AgentToolValidationError reports a tool-scoped validation failure at the
	// agent-as-tool boundary.
	//
	// The runtime converts this error into an `invalid_arguments` tool result with
	// structured retry guidance instead of failing the workflow.
	AgentToolValidationError struct {
		message      string
		issues       []*tools.FieldIssue
		descriptions map[string]string
	}
)

// NewAgentToolValidationError constructs a structured validation error for an
// agent-as-tool pre-child validator.
func NewAgentToolValidationError(message string, issues []*tools.FieldIssue, descriptions map[string]string) *AgentToolValidationError {
	return &AgentToolValidationError{
		message:      message,
		issues:       cloneFieldIssues(issues),
		descriptions: cloneStringMap(descriptions),
	}
}

// Error implements the error interface.
func (e *AgentToolValidationError) Error() string {
	return e.message
}

// Issues returns the structured field issues associated with this validation failure.
func (e *AgentToolValidationError) Issues() []*tools.FieldIssue {
	return cloneFieldIssues(e.issues)
}

// Descriptions returns optional human-readable descriptions for the invalid fields.
func (e *AgentToolValidationError) Descriptions() map[string]string {
	return cloneStringMap(e.descriptions)
}

func cloneFieldIssues(in []*tools.FieldIssue) []*tools.FieldIssue {
	if len(in) == 0 {
		return nil
	}
	out := make([]*tools.FieldIssue, 0, len(in))
	for _, issue := range in {
		if issue == nil {
			continue
		}
		clone := &tools.FieldIssue{
			Field:      issue.Field,
			Constraint: issue.Constraint,
		}
		if len(issue.Allowed) > 0 {
			clone.Allowed = append([]string(nil), issue.Allowed...)
		}
		out = append(out, clone)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// WithText sets plain text content for the given tool ID. The runtime treats the
// text as the user message for that tool. Exactly one of WithText or WithTemplate
// should be provided per tool across all options.
func WithText(id tools.Ident, s string) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.Texts == nil {
			c.Texts = make(map[tools.Ident]string)
		}
		c.Texts[id] = s
	}
}

// WithTemplate sets a compiled template for the given tool ID. The template is
// executed with the tool payload as the root value to produce the user message.
func WithTemplate(id tools.Ident, t *template.Template) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.Templates == nil {
			c.Templates = make(map[tools.Ident]*template.Template)
		}
		c.Templates[id] = t
	}
}

// WithPromptSpec configures a prompt registry ID for the given tool ID.
func WithPromptSpec(id tools.Ident, promptID prompt.Ident) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.PromptSpecs == nil {
			c.PromptSpecs = make(map[tools.Ident]prompt.Ident)
		}
		c.PromptSpecs[id] = promptID
	}
}

// WithTextAll applies the same text to all provided tool IDs.
func WithTextAll(ids []tools.Ident, s string) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.Texts == nil {
			c.Texts = make(map[tools.Ident]string)
		}
		for _, id := range ids {
			c.Texts[id] = s
		}
	}
}

// WithTemplateAll applies the same template to all provided tool IDs.
func WithTemplateAll(ids []tools.Ident, t *template.Template) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.Templates == nil {
			c.Templates = make(map[tools.Ident]*template.Template)
		}
		for _, id := range ids {
			c.Templates[id] = t
		}
	}
}

// NewAgentToolsetRegistration creates a toolset registration for an agent-as-tool.
// The returned registration executes the provider agent as a child workflow using
// ExecuteAgentChildWithRoute, with optional per-tool system prompts/templates.
//
// Callers should set Name/Description/Specs/TaskQueue on the returned registration
// before registering it with the runtime.
func NewAgentToolsetRegistration(rt *Runtime, cfg AgentToolConfig) ToolsetRegistration {
	return ToolsetRegistration{
		Name:        cfg.Name,
		Description: cfg.Description,
		TaskQueue:   cfg.TaskQueue,
		Inline:      true,
		Execute:     defaultAgentToolExecute(rt, cfg),
		AgentTool:   &cfg,
	}
}

// CompileAgentToolTemplates compiles per-tool message templates from plain
// strings into text/template instances. The compiler installs a conservative
// default configuration:
//   - template.Option("missingkey=error") to fail fast on missing fields
//   - a small helper FuncMap containing "tojson" and "join"
//
// The function is a convenience for applications that want to supply template
// text rather than constructing templates and func maps manually. Callers may
// extend the default helpers by passing additional functions via userFuncs.
//
// Use this helper when you intend to register agent-tools with template-based
// user messages (via WithTemplate/WithTemplateAll in generated packages). If
// you prefer to build templates yourself, you can skip this helper entirely
// and pass compiled templates directly.
//
// Returns a map keyed by fully qualified tool IDs. An error is returned if the
// input is empty or any template fails to parse.
func CompileAgentToolTemplates(raw map[tools.Ident]string, userFuncs template.FuncMap) (map[tools.Ident]*template.Template, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("no templates provided")
	}
	funcs := template.FuncMap{
		"tojson": func(v any) (string, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
		"join": strings.Join,
	}
	maps.Copy(funcs, userFuncs)
	compiled := make(map[tools.Ident]*template.Template, len(raw))
	for id, src := range raw {
		name := string(id)
		tmpl, err := template.New(name).Funcs(funcs).Option("missingkey=error").Parse(src)
		if err != nil {
			return nil, fmt.Errorf("compile template for %s: %w", id, err)
		}
		compiled[id] = tmpl
	}
	return compiled, nil
}

// ValidateAgentToolTemplates ensures that templates exist for all provided tool IDs
// and performs a dry-run execution against a zero value representative of the
// payload shape to catch missing keys early.
//
// For primitive/array/map payloads, callers should pass a suitable zero/root; when
// unknown, nil is acceptable and authors should reference {{.}} accordingly.
func ValidateAgentToolTemplates(templates map[tools.Ident]*template.Template, toolIDs []tools.Ident, zeroByTool map[tools.Ident]any) error {
	for _, id := range toolIDs {
		tmpl := templates[id]
		if tmpl == nil {
			return fmt.Errorf("missing template for tool %s", id)
		}
		var b strings.Builder
		if err := tmpl.Execute(&b, zeroByTool[id]); err != nil {
			return fmt.Errorf("template validation failed for %s: %w", id, err)
		}
	}
	return nil
}

// ValidateAgentToolCoverage verifies that every tool in toolIDs has exactly one
// configured content source across texts and templates. Returns an error if a
// tool is missing content or provided in both maps.
func ValidateAgentToolCoverage(texts map[tools.Ident]string, templates map[tools.Ident]*template.Template, toolIDs []tools.Ident) error {
	for _, id := range toolIDs {
		_, hasText := texts[id]
		_, hasTpl := templates[id]
		if hasText && hasTpl {
			return fmt.Errorf("tool %s configured as both text and template", id)
		}
		if !hasText && !hasTpl {
			return fmt.Errorf("tool %s missing text/template content", id)
		}
	}
	return nil
}

// PayloadToString converts a tool payload to a string for agent consumption.
// Strings pass through as-is; structured payloads are marshaled to JSON.
func PayloadToString(payload any) (string, error) {
	switch v := payload.(type) {
	case string:
		return v, nil
	case json.RawMessage:
		if len(v) == 0 {
			return "", nil
		}
		return string(v), nil
	case nil:
		return "", nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload as JSON: %w", err)
	}
	return string(b), nil
}

// defaultAgentToolExecute returns the standard Execute function for agent-as-tool
// registrations. It converts the tool payload to messages (respecting per-tool
// prompts), constructs a nested run context from the current tool call, starts
// the provider agent as a child workflow, and adapts the result to a ToolResult.
func defaultAgentToolExecute(rt *Runtime, cfg AgentToolConfig) func(context.Context, *planner.ToolRequest) (*planner.ToolResult, error) {
	return func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		wfCtx := engine.WorkflowContextFromContext(ctx)
		if wfCtx == nil {
			return nil, fmt.Errorf("workflow context not found")
		}
		if cfg.Route.ID == "" {
			return nil, fmt.Errorf("agent tool route is required")
		}
		parentRun := &run.Context{
			RunID:     call.RunID,
			SessionID: call.SessionID,
			TurnID:    call.TurnID,
		}
		messages, nestedRunCtx, err := rt.buildAgentChildRequest(wfCtx.Context(), &cfg, call, nil, parentRun)
		if err != nil {
			return rt.agentToolRequestFailureResult(*call, err)
		}
		if err := rt.publishHook(
			wfCtx.Context(),
			hooks.NewChildRunLinkedEvent(
				call.RunID,
				call.AgentID,
				call.SessionID,
				call.Name,
				call.ToolCallID,
				nestedRunCtx.RunID,
				cfg.AgentID,
			),
			"",
		); err != nil {
			return nil, err
		}
		outPtr, err := rt.ExecuteAgentChildWithRoute(wfCtx, cfg.Route, messages, nestedRunCtx)
		if err != nil {
			return nil, fmt.Errorf("execute agent: %w", err)
		}
		return rt.adaptAgentChildOutput(ctx, &cfg, call, nestedRunCtx, outPtr)
	}
}

func (r *Runtime) agentToolRequestFailureResult(call planner.ToolRequest, err error) (*planner.ToolResult, error) {
	spec, ok := r.toolSpec(call.Name)
	if !ok {
		return nil, fmt.Errorf("agent tool %s requires a registered ToolSpec", call.Name)
	}
	hint := buildRetryHintFromAgentToolRequestError(err, call.Name, &spec)
	if hint == nil {
		return nil, err
	}
	toolErr := planner.NewToolError(err.Error())
	result := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Error:      toolErr,
	}
	hint.RestrictToTool = true
	result.RetryHint = hint
	if _, err := r.materializeToolResult(context.Background(), call, result); err != nil {
		return nil, err
	}
	return result, nil
}

// attachRunLink stamps the parent tool result with a run handle linking to the
// nested agent run that produced it.
func attachRunLink(result *planner.ToolResult, handle *run.Handle) {
	result.RunLink = handle
}

// buildAgentChildRequest constructs the nested agent messages and run context for an
// agent-as-tool invocation based on the tool call and configuration. It decodes the
// payload for prompt/template rendering and records canonical JSON args for the child.
func (r *Runtime) buildAgentChildRequest(ctx context.Context, cfg *AgentToolConfig, call *planner.ToolRequest, messages []*model.Message, parentRun *run.Context) ([]*model.Message, run.Context, error) {
	var zeroCtx run.Context

	// Decode payload for prompt/template rendering. Prefer tool codecs when
	// specs are registered. Agent-as-tool payloads must be validated at the
	// parent boundary; do not fall back to untyped JSON decoding.
	var promptPayload any
	if len(call.Payload) > 0 {
		if _, ok := r.ToolSpec(call.Name); !ok {
			return nil, zeroCtx, fmt.Errorf(
				"agent tool %s requires a registered ToolSpec for payload decoding (missing specs/codecs)",
				call.Name,
			)
		}
		val, err := r.unmarshalToolValue(ctx, call.Name, call.Payload.RawMessage(), true)
		if err != nil {
			return nil, zeroCtx, fmt.Errorf("decode agent tool payload for %s: %w", call.Name, err)
		}
		promptPayload = val
	}
	if cfg.PreChildValidator != nil {
		if err := cfg.PreChildValidator(ctx, &AgentToolValidationInput{
			Call:      call,
			Payload:   promptPayload,
			Messages:  messages,
			ParentRun: parentRun,
		}); err != nil {
			return nil, zeroCtx, err
		}
	}

	// Build messages: optional agent system prompt, then the per-tool user message.
	var childMessages []*model.Message
	if cfg.SystemPrompt != "" {
		if m := newTextAgentMessage(model.ConversationRoleSystem, cfg.SystemPrompt); m != nil {
			childMessages = []*model.Message{m}
		}
	}

	// Build per-tool user message using prompt specs first, then template, then
	// text, then prompt builder.
	var userContent string
	if promptID, ok := cfg.PromptSpecs[call.Name]; ok {
		rendered, err := r.renderAgentToolPrompt(ctx, promptID, call, promptPayload)
		if err != nil {
			return nil, zeroCtx, fmt.Errorf("render prompt for %s: %w", call.Name, err)
		}
		userContent = rendered
	} else if tmpl := cfg.Templates[call.Name]; tmpl != nil {
		var b strings.Builder
		if err := tmpl.Execute(&b, promptPayload); err != nil {
			return nil, zeroCtx, fmt.Errorf("render tool template for %s: %w", call.Name, err)
		}
		userContent = b.String()
	} else if txt, ok := cfg.Texts[call.Name]; ok {
		userContent = txt
	} else if cfg.Prompt != nil {
		userContent = cfg.Prompt(call.Name, promptPayload)
	} else if len(call.Payload) > 0 {
		// Default: build a deterministic user message from the canonical payload.
		//
		// Contract:
		//   - call.Payload is canonical JSON at this boundary (validated by tool codecs).
		//   - Use the raw JSON bytes verbatim, preserving exact schema keys and shape.
		//   - Consumer code that wants a natural-language projection for string payloads
		//     must configure it explicitly via PromptSpecs/Templates/Texts/Prompt.
		userContent = string(call.Payload.RawMessage())
	}
	if m := newTextAgentMessage(model.ConversationRoleUser, userContent); m != nil {
		childMessages = append(childMessages, m)
	} else {
		// No text content; still append an empty user message.
		childMessages = append(childMessages, &model.Message{Role: model.ConversationRoleUser})
	}

	// Build nested run context from explicit ToolRequest fields.
	nestedRunCtx := run.Context{
		Tool:             call.Name,
		RunID:            NestedRunIDForToolCall(call.RunID, call.Name, call.ToolCallID),
		SessionID:        call.SessionID,
		TurnID:           call.TurnID,
		ParentToolCallID: call.ToolCallID,
		ParentRunID:      call.RunID,
		ParentAgentID:    call.AgentID,
		Labels:           cloneLabels(call.Labels),
	}
	// Preserve the canonical payload bytes as child tool args.
	//
	// Contract:
	//   - planner.ToolRequest.Payload is already canonical JSON at this boundary.
	//   - Child run ToolArgs must be exactly the parent payload bytes.
	//   - Re-encoding through payload codecs here is invalid because payload codecs
	//     encode typed values, while this boundary carries canonical raw JSON.
	if len(call.Payload) > 0 {
		nestedRunCtx.ToolArgs = append(rawjson.Message(nil), call.Payload...)
	}

	return childMessages, nestedRunCtx, nil
}

// renderAgentToolPrompt resolves and renders a configured prompt spec for one tool call.
func (r *Runtime) renderAgentToolPrompt(ctx context.Context, promptID prompt.Ident, call *planner.ToolRequest, payload any) (string, error) {
	if promptID == "" {
		return "", errors.New("prompt id is required")
	}
	if r.PromptRegistry == nil {
		return "", errors.New("prompt registry is not configured")
	}
	renderData, err := r.buildPromptTemplateData(ctx, call.Name, payload)
	if err != nil {
		return "", fmt.Errorf("build prompt template data for %s: %w", call.Name, err)
	}
	renderContext := withPromptRenderHookContext(ctx, PromptRenderHookContext{
		RunID:     call.RunID,
		AgentID:   call.AgentID,
		SessionID: call.SessionID,
		TurnID:    call.TurnID,
	})
	content, err := r.PromptRegistry.Render(renderContext, promptID, prompt.Scope{
		SessionID: call.SessionID,
		Labels:    cloneLabels(call.Labels),
	}, renderData)
	if err != nil {
		return "", err
	}
	return content.Text, nil
}

// buildPromptTemplateData converts a typed tool payload into canonical prompt
// template data.
//
// Contract:
//   - Payload must be a typed Go value decoded by the generated payload codec.
//   - Prompt templates render against the canonical JSON object produced by the
//     same payload codec (schema keys, e.g. snake_case).
//   - Non-object payload JSON shapes are rejected to keep the template contract
//     explicit and deterministic.
func (r *Runtime) buildPromptTemplateData(ctx context.Context, toolName tools.Ident, payload any) (map[string]any, error) {
	if payload == nil {
		return map[string]any{}, nil
	}
	switch payload.(type) {
	case json.RawMessage, []byte:
		return nil, fmt.Errorf("tool %s prompt payload must be a typed Go value, got %T", toolName, payload)
	}
	codec, ok := r.toolCodec(toolName, true)
	if !ok || codec.ToJSON == nil {
		r.logger.Error(ctx, "no codec found for tool", "tool", toolName, "payload", true)
		return nil, fmt.Errorf("no codec found for tool %s", toolName)
	}
	raw, err := codec.ToJSON(payload)
	if err != nil {
		r.logger.Warn(ctx, "tool codec encode failed", "tool", toolName, "payload", true, "err", err)
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]any{}, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool %s prompt payload must render from a JSON object, got %T", toolName, decoded)
	}
	return object, nil
}

// adaptAgentChildOutput converts a nested agent RunOutput into a planner.ToolResult.
//
// Preference order:
//   - Use the child-produced FinalToolResult when present.
//   - Otherwise convert the child run output into the legacy prose-oriented
//     planner.ToolResult shape.
//
// In all cases the returned ToolResult is linked back to the child run.
func (r *Runtime) adaptAgentChildOutput(ctx context.Context, cfg *AgentToolConfig, call *planner.ToolRequest, nestedRunCtx run.Context, outPtr *RunOutput) (*planner.ToolResult, error) {
	if outPtr == nil {
		return nil, fmt.Errorf("execute agent returned no output")
	}

	handle := &run.Handle{
		RunID:            nestedRunCtx.RunID,
		AgentID:          cfg.AgentID,
		ParentRunID:      nestedRunCtx.ParentRunID,
		ParentToolCallID: nestedRunCtx.ParentToolCallID,
	}

	if outPtr.FinalToolResult != nil {
		tr, err := r.decodeAgentChildFinalToolResult(ctx, call, outPtr.FinalToolResult)
		if err != nil {
			return nil, err
		}
		tr.ToolCallID = call.ToolCallID
		tr.ChildrenCount = len(outPtr.ToolEvents)
		attachRunLink(tr, handle)
		return tr, nil
	}

	result := ConvertRunOutputToToolResult(call.Name, outPtr)
	result.ToolCallID = call.ToolCallID
	attachRunLink(&result, handle)
	tr := &result
	return tr, nil
}

// decodeAgentChildFinalToolResult decodes the workflow-safe final tool-result
// envelope emitted by a nested child run into the parent planner.ToolResult
// shape.
func (r *Runtime) decodeAgentChildFinalToolResult(ctx context.Context, call *planner.ToolRequest, event *api.ToolEvent) (*planner.ToolResult, error) {
	if call == nil {
		return nil, errors.New("agent-tool final result: tool call is required")
	}
	if event == nil {
		return nil, fmt.Errorf("agent-tool final result for %s: event is nil", call.Name)
	}
	result := &planner.ToolResult{
		Name:                call.Name,
		ResultBytes:         event.ResultBytes,
		ResultOmitted:       event.ResultOmitted,
		ResultOmittedReason: event.ResultOmittedReason,
		ServerData:          append(rawjson.Message(nil), event.ServerData...),
		Bounds:              event.Bounds,
		Error:               event.Error,
		RetryHint:           event.RetryHint,
		Telemetry:           event.Telemetry,
	}
	if hasNonNullJSON(event.Result.RawMessage()) && event.Error == nil {
		decoded, err := r.unmarshalToolValue(ctx, call.Name, event.Result.RawMessage(), false)
		if err != nil {
			return nil, fmt.Errorf("decode final tool result for %s: %w", call.Name, err)
		}
		result.Result = decoded
	}
	return result, nil
}
