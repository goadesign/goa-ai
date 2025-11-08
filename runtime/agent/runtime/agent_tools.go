package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"text/template"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
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
	// maps fully-qualified tool names (e.g., "service.toolset.tool") to system
	// prompts that will be prepended to the nested agent messages for that tool.
	AgentToolConfig struct {
		// AgentID is the fully qualified identifier of the nested agent.
		AgentID agent.Ident
		// Route provides strong-contract routing metadata for cross-process inline
		// execution. When set, the runtime uses this route (workflow + queue) and
		// the provided activity names to orchestrate the nested agent. When empty,
		// inline execution must rely on a locally registered agent; otherwise the
		// runtime returns an error (no fallbacks).
		Route AgentRoute
		// PlanActivityName is the fully-qualified plan activity name for the nested agent.
		PlanActivityName string
		// ResumeActivityName is the fully-qualified resume activity name for the nested agent.
		ResumeActivityName string
		// ExecuteToolActivity is the fully-qualified execute_tool activity name for the nested agent.
		ExecuteToolActivity string
		// SystemPrompt, when non-empty, is prepended as a system message for all tools.
		SystemPrompt string
		// Templates maps fully-qualified tool IDs to compiled templates used to render
		// the tool-specific user message from the tool payload. Templates MUST be
		// provided for all tools in this toolset and are compiled with
		// template.Option("missingkey=error").
		Templates map[tools.Ident]*template.Template
		// Texts maps fully-qualified tool IDs to a pure text user message. When a
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
		// Aggregate, when set, is invoked once after the nested agent finishes to
		// construct the parent tool_result from the child tool results of the nested run.
		// If nil, the runtime falls back to ConvertRunOutputToToolResult.
		Aggregate func(ctx context.Context, parent ParentCall, children []ChildCall) (planner.ToolResult, error)
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

// NewAgentToolsetRegistration creates a toolset registration for an agent-as-tool.
// The returned registration has its Execute function wired to run the nested agent
// inline using ExecuteAgentInline with optional per-tool system prompts.
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
	}
}

// ParentCall identifies the parent tool call in an agent-as-tool execution.
type ParentCall struct {
	ToolName   tools.Ident
	ToolCallID string
}

// ChildCall summarizes a child tool outcome from a nested run used for aggregation.
type ChildCall struct {
	ToolName   tools.Ident
	ToolCallID string
	Status     string // "ok" | "error"
	Result     any
	Error      error
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
func CompileAgentToolTemplates(
	raw map[tools.Ident]string, userFuncs template.FuncMap,
) (map[tools.Ident]*template.Template, error) {
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
		tmpl, err := template.New(name).
			Funcs(funcs).
			Option("missingkey=error").
			Parse(src)
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
func ValidateAgentToolTemplates(
	templates map[tools.Ident]*template.Template,
	toolIDs []tools.Ident,
	zeroByTool map[tools.Ident]any,
) error {
	for _, id := range toolIDs {
		tmpl := templates[id]
		if tmpl == nil {
			return fmt.Errorf("missing template for tool %s", id)
		}
		var b strings.Builder
		if err := tmpl.Execute(&b, zeroByTool[id]); err != nil {
			return fmt.Errorf(
				"template validation failed for %s: %w", id, err,
			)
		}
	}
	return nil
}

// ValidateAgentToolCoverage verifies that every tool in toolIDs has exactly one
// configured content source across texts and templates. Returns an error if a
// tool is missing content or provided in both maps.
func ValidateAgentToolCoverage(
	texts map[tools.Ident]string,
	templates map[tools.Ident]*template.Template,
	toolIDs []tools.Ident,
) error {
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
	if s, ok := payload.(string); ok {
		return s
	}
	if payload == nil {
		return ""
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("%v", payload)
	}
	return string(b)
}

// defaultAgentToolExecute returns the standard Execute function for agent-as-tool
// registrations. It converts the tool payload to messages (respecting per-tool
// system prompts when configured), constructs a nested run context derived from
// the current tool call, executes the nested agent inline, and adapts the result
// to a planner.ToolResult.
func defaultAgentToolExecute(
	rt *Runtime, cfg AgentToolConfig,
) func(context.Context, planner.ToolRequest) (planner.ToolResult, error) {
	return func(
		ctx context.Context, call planner.ToolRequest,
	) (planner.ToolResult, error) {
		wfCtx := engine.WorkflowContextFromContext(ctx)
		if wfCtx == nil {
			return planner.ToolResult{}, fmt.Errorf("workflow context not found")
		}

		// Build messages: optional agent system prompt, then the per-tool user message
		var messages []planner.AgentMessage
		if strings.TrimSpace(cfg.SystemPrompt) != "" {
			messages = append(
				messages,
				planner.AgentMessage{
					Role:    "system",
					Content: cfg.SystemPrompt,
				},
			)
		}

		// Build per-tool user message via template if present, otherwise fall back to text
		if tmpl := cfg.Templates[call.Name]; tmpl != nil {
			var b strings.Builder
			if err := tmpl.Execute(&b, call.Payload); err != nil {
				return planner.ToolResult{}, fmt.Errorf(
					"render tool template for %s: %w",
					call.Name, err,
				)
			}
			messages = append(messages, planner.AgentMessage{
				Role:    "user",
				Content: b.String(),
			})
		} else if txt, ok := cfg.Texts[call.Name]; ok {
			messages = append(messages, planner.AgentMessage{
				Role:    "user",
				Content: txt,
			})
		} else {
			// Default: build from payload via PromptBuilder or JSON/string fallback
			if cfg.Prompt != nil {
				messages = append(messages, planner.AgentMessage{
					Role:    "user",
					Content: cfg.Prompt(call.Name, call.Payload),
				})
			} else {
				messages = append(messages, planner.AgentMessage{
					Role:    "user",
					Content: PayloadToString(call.Payload),
				})
			}
		}

		// Build nested run context from explicit ToolRequest fields
		nestedRunCtx := run.Context{
			RunID:            NestedRunID(call.RunID, call.Name),
			SessionID:        call.SessionID,
			TurnID:           call.TurnID,
			ParentToolCallID: call.ToolCallID,
		}

		var outPtr *RunOutput
		var err error
		if cfg.Route.ID != "" {
			// Strong contract: use explicit route + activity names; no fallbacks.
			outPtr, err = rt.ExecuteAgentInlineWithRoute(
				wfCtx,
				cfg.Route,
				cfg.PlanActivityName,
				cfg.ResumeActivityName,
				cfg.ExecuteToolActivity,
				messages,
				nestedRunCtx,
			)
		} else {
			// Require local registration; avoid synthetic conventions.
			// ExecuteAgentInline will look up the local agent; if not present,
			// return a clear error rather than guessing.
			outPtr, err = rt.ExecuteAgentInline(wfCtx, string(cfg.AgentID), messages, nestedRunCtx)
		}
		if err != nil {
			return planner.ToolResult{}, fmt.Errorf("execute agent inline: %w", err)
		}

		if outPtr == nil {
			return planner.ToolResult{}, fmt.Errorf("execute agent inline returned no output")
		}
		// Aggregation path: assemble parent tool_result from child results.
		if cfg.Aggregate != nil {
			// Build children from the nested run's ToolEvents (last turn results).
			children := make([]ChildCall, 0, len(outPtr.ToolEvents))
			for _, ev := range outPtr.ToolEvents {
				if ev == nil {
					continue
				}
				status := "ok"
				if ev.Error != nil {
					status = "error"
				}
				children = append(children, ChildCall{
					ToolName:   ev.Name,
					ToolCallID: ev.ToolCallID,
					Status:     status,
					Result:     ev.Result,
					Error:      ev.Error,
				})
			}
			parent := ParentCall{
				ToolName:   call.Name,
				ToolCallID: call.ToolCallID,
			}
			tr, aerr := cfg.Aggregate(ctx, parent, children)
			if aerr == nil {
				return tr, nil
			}
			// Fall back to default conversion on aggregation failure.
		}
		// JSON-only structured result default: aggregate child results into a structured payload
		// instead of returning the nested agent's final prose. This produces a consistent,
		// schema-like output across service-backed and agent-as-tool paths.
		if cfg.JSONOnly {
			// If exactly one child tool result exists, pass its result through; otherwise
			// return an array preserving order. Errors and telemetry are aggregated below.
			var payload any
			switch n := len(outPtr.ToolEvents); {
			case n == 1 && outPtr.ToolEvents[0] != nil:
				payload = outPtr.ToolEvents[0].Result
			case n > 1:
				items := make([]any, 0, n)
				for _, ev := range outPtr.ToolEvents {
					if ev == nil {
						continue
					}
					items = append(items, ev.Result)
				}
				payload = items
			default:
				// No child tools: return the final message content to avoid empty payloads
				payload = outPtr.Final.Content
			}
			// Aggregate telemetry similar to ConvertRunOutputToToolResult
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
			tr := planner.ToolResult{
				Name:      call.Name,
				Result:    payload,
				Telemetry: tel,
			}
			if errCount > 0 && errCount == len(outPtr.ToolEvents) {
				tr.Error = planner.NewToolErrorWithCause("agent-tool: all nested tools failed", lastErr)
			}
			return tr, nil
		}
		return ConvertRunOutputToToolResult(call.Name, *outPtr), nil
	}
}
