package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"text/template"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// AgentToolOption configures per-tool content for agent-as-tool registrations.
	// Options are applied to AgentToolConfig before constructing the registration.
	AgentToolOption func(*AgentToolConfig)

	// PromptBuilder builds a user message for a tool call from its payload when
	// no explicit text or template is configured.
	PromptBuilder func(id tools.Ident, payload any) string

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
		// Prompt builds a user message when neither text nor template is provided.
		// When nil, the runtime falls back to PayloadToString(payload).
		Prompt PromptBuilder
		// JSONOnly forces JSON-only parent tool_result emission for agent-as-tool.
		// When true (default), the runtime ignores the nested agent's final prose and
		// uses the aggregator output as the parent tool_result.
		JSONOnly bool
		// Finalizer, when set, executes once after the nested agent finishes to
		// construct the parent tool_result from the child tool results of the nested run.
		// If nil, the runtime falls back to ConvertRunOutputToToolResult.
		Finalizer Finalizer
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
		// AggregateKeys maps tool IDs to the JSON key for merging their child results.
		// When a tool produces multiple child results (n > 1) and has an entry here:
		//   - If each child result is an object containing that key with an array value,
		//     the arrays are merged into a single array under that key.
		//   - Otherwise, child results are wrapped: {key: [child1, child2, ...]}.
		// When a tool is not in this map and n > 1, defaults to wrapping under "results".
		// This ensures Bedrock compatibility (toolResult.json must be an object).
		AggregateKeys map[tools.Ident]string
	}

	// ParentCall identifies the parent tool call in an agent-as-tool execution.
	ParentCall struct {
		// ToolName is the fully-qualified identifier of the parent tool.
		ToolName tools.Ident
		// ToolCallID is the provider/tool-call correlation identifier for this tool invocation.
		ToolCallID string
		// Payload is the decoded tool payload for the parent call when available.
		// It is nil when the parent tool had an empty payload.
		Payload any
		// ArtifactsMode is the normalized per-call artifacts toggle selected by
		// the caller via the reserved `artifacts` payload field.
		ArtifactsMode tools.ArtifactsMode
	}

	// ChildCall summarizes a child tool outcome from a nested run used for aggregation.
	ChildCall struct {
		ToolName   tools.Ident
		ToolCallID string
		Status     string // "ok" | "error"
		Result     any
		Error      error
	}

	// Finalizer produces the parent tool_result for nested agent executions.
	Finalizer interface {
		Finalize(ctx context.Context, input *FinalizerInput) (*planner.ToolResult, error)
	}

	// FinalizerFunc adapts a function to the Finalizer interface.
	FinalizerFunc func(ctx context.Context, input *FinalizerInput) (*planner.ToolResult, error)

	// FinalizerInput captures aggregation context for Finalize.
	FinalizerInput struct {
		Parent   ParentCall
		Children []ChildCall
		// Invoke executes additional tools deterministically during aggregation. It
		// is nil when tool-based finalization is unavailable (e.g., service tools
		// invoked outside a workflow context).
		Invoke ToolInvoker
	}

	// ToolPayloadBuilder constructs the payload passed to a tool-based finalizer.
	ToolPayloadBuilder func(ctx context.Context, input *FinalizerInput) (any, error)

	// ToolInvoker executes registered tool calls on behalf of a finalizer.
	ToolInvoker interface {
		Invoke(ctx context.Context, tool tools.Ident, payload any) (*planner.ToolResult, error)
	}

	// ToolInvokerFunc adapts a function to ToolInvoker.
	ToolInvokerFunc func(ctx context.Context, tool tools.Ident, payload any) (*planner.ToolResult, error)
)

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

// WithAggregateKey sets the JSON key for merging multiple child results for the
// given tool ID. When the tool produces n > 1 child results and each child has
// an array under this key, the arrays are merged. Otherwise, results are wrapped
// under this key.
func WithAggregateKey(id tools.Ident, key string) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.AggregateKeys == nil {
			c.AggregateKeys = make(map[tools.Ident]string)
		}
		c.AggregateKeys[id] = key
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
	}
	return nil
}

// PayloadToString converts a tool payload to a string for agent consumption.
// Strings pass through as-is; structured payloads are marshaled to JSON.
func PayloadToString(payload any) string {
	switch v := payload.(type) {
	case string:
		return v
	case json.RawMessage:
		if len(v) == 0 {
			return ""
		}
		return string(v)
	case nil:
		return ""
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("%v", payload)
	}
	return string(b)
}

// PassThroughFinalizer returns a finalizer that leaves aggregation to the JSONOnly fallback.
func PassThroughFinalizer() Finalizer {
	return FinalizerFunc(func(context.Context, *FinalizerInput) (*planner.ToolResult, error) {
		return &planner.ToolResult{}, nil
	})
}

// ToolResultFinalizer returns a finalizer that delegates aggregation to a dedicated tool.
// The builder constructs the tool payload from the parent/child calls; the configured
// ToolInvoker executes the aggregation tool and the resulting ToolResult becomes the
// parent tool_result (the runtime overwrites Name/ToolCallID for correlation).
func ToolResultFinalizer(tool tools.Ident, builder ToolPayloadBuilder) Finalizer {
	return FinalizerFunc(func(ctx context.Context, input *FinalizerInput) (*planner.ToolResult, error) {
		if input.Invoke == nil {
			return nil, fmt.Errorf("tool finalizer for %s: tool invoker unavailable", tool)
		}
		if tool == "" {
			return nil, errors.New("tool finalizer: tool identifier is required")
		}
		if builder == nil {
			return nil, errors.New("tool finalizer: payload builder is required")
		}
		payload, err := builder(ctx, input)
		if err != nil {
			return nil, err
		}
		result, err := input.Invoke.Invoke(ctx, tool, payload)
		if err != nil {
			return nil, err
		}
		return result, nil
	})
}

// Finalize satisfies the Finalizer interface.
func (f FinalizerFunc) Finalize(ctx context.Context, input *FinalizerInput) (*planner.ToolResult, error) {
	return f(ctx, input)
}

// Invoke satisfies the ToolInvoker interface.
func (f ToolInvokerFunc) Invoke(ctx context.Context, tool tools.Ident, payload any) (*planner.ToolResult, error) {
	return f(ctx, tool, payload)
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
		messages, nestedRunCtx, err := rt.buildAgentChildRequest(wfCtx.Context(), &cfg, call)
		if err != nil {
			return nil, err
		}
		rt.publishHook(
			wfCtx.Context(),
			hooks.NewAgentRunStartedEvent(
				call.RunID,
				call.AgentID,
				call.SessionID,
				call.Name,
				call.ToolCallID,
				nestedRunCtx.RunID,
				cfg.AgentID,
			),
			"",
		)
		outPtr, err := rt.ExecuteAgentChildWithRoute(wfCtx, cfg.Route, messages, nestedRunCtx)
		if err != nil {
			return nil, fmt.Errorf("execute agent: %w", err)
		}
		return rt.adaptAgentChildOutput(ctx, &cfg, call, nestedRunCtx, outPtr)
	}
}

// attachRunLink stamps the parent tool result and any attached artifacts with
// a run handle linking to the nested agent run that produced them.
func attachRunLink(result *planner.ToolResult, handle *run.Handle) {
	result.RunLink = handle
	for i := range result.Artifacts {
		if result.Artifacts[i].RunLink != nil {
			continue
		}
		result.Artifacts[i].RunLink = handle
	}
}

// mergeByKey extracts the array value under key from each item and merges them.
// Returns the merged slice and true if all items are objects with key containing arrays.
func mergeByKey(items []any, key string) ([]any, bool) {
	var merged []any
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		val, exists := obj[key]
		if !exists {
			return nil, false
		}
		arr, ok := val.([]any)
		if !ok {
			return nil, false
		}
		merged = append(merged, arr...)
	}
	return merged, true
}

func mergeByKeyFromChildResults(ctx context.Context, rt *Runtime, events []*planner.ToolResult, key string) ([]any, error) {
	items := make([]any, 0, len(events))
	for _, ev := range events {
		raw, err := rt.marshalToolValue(ctx, ev.Name, ev.Result, false)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no child results to merge")
	}
	merged, ok := mergeByKey(items, key)
	if !ok {
		return nil, fmt.Errorf("child results do not all contain %q array", key)
	}
	return merged, nil
}

// buildAgentChildRequest constructs the nested agent messages and run context for an
// agent-as-tool invocation based on the tool call and configuration. It decodes the
// payload for prompt/template rendering and records canonical JSON args for the child.
func (r *Runtime) buildAgentChildRequest(ctx context.Context, cfg *AgentToolConfig, call *planner.ToolRequest) ([]*model.Message, run.Context, error) {
	var zeroCtx run.Context

	// Decode payload for prompt/template rendering. Prefer tool codecs when
	// specs are registered; otherwise decode as generic JSON.
	var promptPayload any
	if len(call.Payload) > 0 {
		if _, ok := r.ToolSpec(call.Name); ok {
			val, err := r.unmarshalToolValue(ctx, call.Name, call.Payload, true)
			if err != nil {
				return nil, zeroCtx, fmt.Errorf("decode agent tool payload for %s: %w", call.Name, err)
			}
			promptPayload = val
		} else {
			var generic any
			if err := json.Unmarshal(call.Payload, &generic); err != nil {
				return nil, zeroCtx, fmt.Errorf("decode agent tool payload for %s: %w", call.Name, err)
			}
			promptPayload = generic
		}
	}

	// Build messages: optional agent system prompt, then the per-tool user message.
	var messages []*model.Message
	if cfg.SystemPrompt != "" {
		if m := newTextAgentMessage(model.ConversationRoleSystem, cfg.SystemPrompt); m != nil {
			messages = []*model.Message{m}
		}
	}

	// Build per-tool user message via template if present, otherwise fall
	// back to text/prompt/payload. Skip appending when the content is empty
	// or a meaningless JSON shell ("{}" / "null").
	var userContent string
	if tmpl := cfg.Templates[call.Name]; tmpl != nil {
		var b strings.Builder
		if err := tmpl.Execute(&b, promptPayload); err != nil {
			return nil, zeroCtx, fmt.Errorf("render tool template for %s: %w", call.Name, err)
		}
		userContent = b.String()
	} else if txt, ok := cfg.Texts[call.Name]; ok {
		userContent = txt
	} else {
		// Default: build from payload via PromptBuilder or JSON/string fallback.
		if cfg.Prompt != nil {
			userContent = cfg.Prompt(call.Name, promptPayload)
		} else {
			userContent = PayloadToString(promptPayload)
		}
	}
	switch userContent {
	case "{}", "null":
		// Append an empty user message to preserve turn semantics.
		messages = append(messages, &model.Message{Role: model.ConversationRoleUser})
	default:
		if m := newTextAgentMessage(model.ConversationRoleUser, userContent); m != nil {
			messages = append(messages, m)
		} else {
			// No text content; still append an empty user message.
			messages = append(messages, &model.Message{Role: model.ConversationRoleUser})
		}
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
	}
	// Record the canonical JSON args using the tool codec. marshalToolValue
	// returns a defensive copy for json.RawMessage, so this never double-encodes.
	if argsJSON, err := r.marshalToolValue(ctx, call.Name, call.Payload, true); err == nil && len(argsJSON) > 0 {
		nestedRunCtx.ToolArgs = argsJSON
	}

	return messages, nestedRunCtx, nil
}

// adaptAgentChildOutput converts a nested agent RunOutput into a planner.ToolResult,
// applying optional Finalizer/JSONOnly aggregation and attaching a run link so
// callers can correlate parent tool calls with child runs.
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

	// Aggregation path: assemble parent tool_result from child results.
	if cfg.Finalizer != nil {
		return r.adaptWithFinalizer(ctx, cfg, call, outPtr, handle)
	}

	// JSON-only structured result default: aggregate child results into a structured
	// payload instead of returning the nested agent's final prose.
	if cfg.JSONOnly {
		return r.adaptWithJSONOnly(ctx, cfg, call, outPtr, handle)
	}

	result := ConvertRunOutputToToolResult(call.Name, outPtr)
	result.ToolCallID = call.ToolCallID
	attachRunLink(&result, handle)
	return &result, nil
}

// adaptWithFinalizer applies the configured Finalizer to produce a parent tool_result
// from child results. If the finalizer fails, returns an error result with the cause.
func (r *Runtime) adaptWithFinalizer(ctx context.Context, cfg *AgentToolConfig, call *planner.ToolRequest, outPtr *RunOutput, handle *run.Handle) (*planner.ToolResult, error) {
	children := make([]ChildCall, 0, len(outPtr.ToolEvents))
	for _, ev := range outPtr.ToolEvents {
		if ev == nil {
			continue
		}
		status := "ok"
		var childErr error
		if ev.Error != nil {
			status = "error"
			childErr = ev.Error
		}
		children = append(children, ChildCall{
			ToolName:   ev.Name,
			ToolCallID: ev.ToolCallID,
			Status:     status,
			Result:     ev.Result,
			Error:      childErr,
		})
	}

	var parentPayload any
	if len(call.Payload) > 0 {
		if _, ok := r.ToolSpec(call.Name); ok {
			val, err := r.unmarshalToolValue(ctx, call.Name, call.Payload, true)
			if err != nil {
				return nil, fmt.Errorf("decode parent tool payload for %s: %w", call.Name, err)
			}
			parentPayload = val
		} else {
			var generic any
			if err := json.Unmarshal(call.Payload, &generic); err != nil {
				return nil, fmt.Errorf("decode parent tool payload for %s: %w", call.Name, err)
			}
			parentPayload = generic
		}
	}

	parent := ParentCall{
		ToolName:      call.Name,
		ToolCallID:    call.ToolCallID,
		Payload:       parentPayload,
		ArtifactsMode: call.ArtifactsMode,
	}
	invoker := finalizerToolInvokerFromContext(ctx, call)
	input := FinalizerInput{
		Parent:   parent,
		Children: children,
		Invoke:   invoker,
	}
	tr, err := cfg.Finalizer.Finalize(ctx, &input)
	if err != nil {
		result := &planner.ToolResult{
			Name:          call.Name,
			ToolCallID:    call.ToolCallID,
			Error:         planner.NewToolErrorWithCause("agent-tool: finalizer failed", err),
			Artifacts:     aggregateArtifacts(outPtr.ToolEvents),
			ChildrenCount: len(outPtr.ToolEvents),
		}
		attachRunLink(result, handle)
		return result, nil
	}
	if tr.Result != nil {
		tr.Name = call.Name
		tr.ToolCallID = call.ToolCallID
		tr.ChildrenCount = len(outPtr.ToolEvents)
		tr.Artifacts = append(tr.Artifacts, aggregateArtifacts(outPtr.ToolEvents)...)
		attachRunLink(tr, handle)
		return tr, nil
	}
	// Finalizer returned empty result, fall through to default conversion.
	result := ConvertRunOutputToToolResult(call.Name, outPtr)
	result.ToolCallID = call.ToolCallID
	attachRunLink(&result, handle)
	return &result, nil
}

// adaptWithJSONOnly aggregates child results into a structured JSON payload.
// This produces a consistent, schema-like output across service-backed and
// agent-as-tool paths.
func (r *Runtime) adaptWithJSONOnly(ctx context.Context, cfg *AgentToolConfig, call *planner.ToolRequest, outPtr *RunOutput, handle *run.Handle) (*planner.ToolResult, error) {
	var (
		payload any
		aggErr  error
	)
	switch n := len(outPtr.ToolEvents); {
	case n == 1 && outPtr.ToolEvents[0] != nil:
		// Validate by round-tripping through the parent tool codec. This ensures
		// that result hint templates and UIs always see the parent tool's schema shape.
		raw, err := r.marshalToolValue(ctx, call.Name, outPtr.ToolEvents[0].Result, false)
		if err != nil {
			aggErr = err
			break
		}
		typed, err := r.unmarshalToolValue(ctx, call.Name, raw, false)
		if err != nil {
			aggErr = err
			break
		}
		payload = typed
	case n > 1:
		aggregateKey := cfg.AggregateKeys[call.Name]
		if aggregateKey == "" {
			aggErr = fmt.Errorf(
				"JSONOnly cannot aggregate %d child results for %s without an explicit Finalizer or AggregateKey",
				n, call.Name,
			)
			break
		}
		merged, err := mergeByKeyFromChildResults(ctx, r, outPtr.ToolEvents, aggregateKey)
		if err != nil {
			aggErr = err
			break
		}
		aggRaw, err := json.Marshal(map[string]any{aggregateKey: merged})
		if err != nil {
			aggErr = err
			break
		}
		typed, err := r.unmarshalToolValue(ctx, call.Name, json.RawMessage(aggRaw), false)
		if err != nil {
			aggErr = err
			break
		}
		payload = typed
	default:
		aggErr = fmt.Errorf("JSONOnly produced no child results for %s", call.Name)
	}

	// Aggregate telemetry from child events.
	var tel *telemetry.ToolTelemetry
	if len(outPtr.ToolEvents) > 0 {
		var totalTokens int
		var totalDurationMs int64
		for _, ev := range outPtr.ToolEvents {
			if ev == nil || ev.Telemetry == nil {
				continue
			}
			totalTokens += ev.Telemetry.TokensUsed
			totalDurationMs += ev.Telemetry.DurationMs
		}
		if totalTokens > 0 || totalDurationMs > 0 {
			tel = &telemetry.ToolTelemetry{
				TokensUsed: totalTokens,
				DurationMs: totalDurationMs,
			}
		}
	}

	// If all children failed, propagate an error; else success with aggregated payload.
	var errCount int
	var lastErr error
	for _, ev := range outPtr.ToolEvents {
		if ev != nil && ev.Error != nil {
			errCount++
			lastErr = ev.Error
		}
	}
	tr := &planner.ToolResult{
		Name:          call.Name,
		ToolCallID:    call.ToolCallID,
		Result:        payload,
		Telemetry:     tel,
		Artifacts:     aggregateArtifacts(outPtr.ToolEvents),
		ChildrenCount: len(outPtr.ToolEvents),
	}
	attachRunLink(tr, handle)
	if aggErr != nil {
		tr.Error = planner.NewToolErrorWithCause(
			"agent-tool: JSONOnly aggregation failed (tool result schema mismatch; missing finalizer?)",
			aggErr,
		)
		return tr, nil
	}
	if errCount > 0 && errCount == len(outPtr.ToolEvents) {
		tr.Error = planner.NewToolErrorWithCause("agent-tool: all nested tools failed", lastErr)
	}
	return tr, nil
}
