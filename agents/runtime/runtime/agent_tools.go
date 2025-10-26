package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"text/template"

	"goa.design/goa-ai/agents/runtime/engine"
	"goa.design/goa-ai/agents/runtime/planner"
	"goa.design/goa-ai/agents/runtime/run"
	"goa.design/goa-ai/agents/runtime/tools"
)

// AgentToolOption configures per-tool content for agent-as-tool registrations.
// Options are applied to AgentToolConfig before constructing the registration.
type AgentToolOption func(*AgentToolConfig)

// WithText sets plain text content for the given tool ID. The runtime treats the
// text as the user message for that tool. Exactly one of WithText or WithTemplate
// should be provided per tool across all options.
func WithText(id tools.ID, s string) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.Texts == nil {
			c.Texts = make(map[tools.ID]string)
		}
		c.Texts[id] = s
	}
}

// WithTemplate sets a compiled template for the given tool ID. The template is
// executed with the tool payload as the root value to produce the user message.
func WithTemplate(id tools.ID, t *template.Template) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.Templates == nil {
			c.Templates = make(map[tools.ID]*template.Template)
		}
		c.Templates[id] = t
	}
}

// WithTextAll applies the same text to all provided tool IDs.
func WithTextAll(ids []tools.ID, s string) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.Texts == nil {
			c.Texts = make(map[tools.ID]string)
		}
		for _, id := range ids {
			c.Texts[id] = s
		}
	}
}

// WithTemplateAll applies the same template to all provided tool IDs.
func WithTemplateAll(ids []tools.ID, t *template.Template) AgentToolOption {
	return func(c *AgentToolConfig) {
		if c.Templates == nil {
			c.Templates = make(map[tools.ID]*template.Template)
		}
		for _, id := range ids {
			c.Templates[id] = t
		}
	}
}

// AgentToolConfig configures how an agent-tool executes.
//
// AgentID identifies the nested agent to execute. SystemPrompts optionally
// maps fully-qualified tool names (e.g., "service.toolset.tool") to system
// prompts that will be prepended to the nested agent messages for that tool.
type AgentToolConfig struct {
	// AgentID is the fully qualified identifier of the nested agent.
	AgentID string
	// SystemPrompt, when non-empty, is prepended as a system message for all tools.
	SystemPrompt string
	// Templates maps fully-qualified tool IDs to compiled templates used to render
	// the tool-specific user message from the tool payload. Templates MUST be
	// provided for all tools in this toolset and are compiled with
	// template.Option("missingkey=error").
	Templates map[tools.ID]*template.Template
	// Texts maps fully-qualified tool IDs to a pure text user message. When a
	// template for a tool is not provided, the runtime uses the corresponding
	// text if present. Exactly one of Templates[id] or Texts[id] should be set
	// per tool. Callers are responsible for ensuring full coverage across tools.
	Texts map[tools.ID]string
	// Name optionally sets the toolset registration name (qualified toolset id).
	Name string
	// Description optionally describes the toolset.
	Description string
	// TaskQueue optionally sets the task queue for this toolset's activities.
	TaskQueue string
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
		Execute:     defaultAgentToolExecute(rt, cfg),
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
func CompileAgentToolTemplates(
	raw map[tools.ID]string, userFuncs template.FuncMap,
) (map[tools.ID]*template.Template, error) {
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
	compiled := make(map[tools.ID]*template.Template, len(raw))
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
	templates map[tools.ID]*template.Template,
	toolIDs []tools.ID,
	zeroByTool map[tools.ID]any,
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
	texts map[tools.ID]string,
	templates map[tools.ID]*template.Template,
	toolIDs []tools.ID,
) error {
	for _, id := range toolIDs {
		_, hasText := texts[id]
		_, hasTpl := templates[id]
		if hasText && hasTpl {
			return fmt.Errorf("tool %s configured as both text and template", id)
		}
		if !hasText && !hasTpl {
			return fmt.Errorf("missing content for tool %s", id)
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
) func(context.Context, planner.ToolCallRequest) (planner.ToolResult, error) {
	return func(
		ctx context.Context, call planner.ToolCallRequest,
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
				planner.AgentMessage{Role: "system", Content: cfg.SystemPrompt},
			)
		}

		// Build per-tool user message via template if present, otherwise fall back to text
		if tmpl := cfg.Templates[tools.ID(call.Name)]; tmpl != nil {
			var b strings.Builder
			if err := tmpl.Execute(&b, call.Payload); err != nil {
				return planner.ToolResult{}, fmt.Errorf(
					"render tool template for %s: %w",
					call.Name, err,
				)
			}
			messages = append(messages, planner.AgentMessage{Role: "user", Content: b.String()})
		} else if txt, ok := cfg.Texts[tools.ID(call.Name)]; ok {
			messages = append(messages, planner.AgentMessage{Role: "user", Content: txt})
		} else {
			return planner.ToolResult{}, fmt.Errorf("no content configured for tool: %s", call.Name)
		}

		parentToolCallID := call.ToolCallID
		if parentToolCallID == "" {
			parentToolCallID = generateToolCallID(call.RunID, call.Name)
		}

		// Build nested run context from explicit ToolCallRequest fields
		nestedRunCtx := run.Context{
			RunID:            NestedRunID(call.RunID, call.Name),
			SessionID:        call.SessionID,
			TurnID:           call.TurnID,
			ParentToolCallID: parentToolCallID,
		}

		output, err := rt.ExecuteAgentInline(wfCtx, cfg.AgentID, messages, nestedRunCtx)
		if err != nil {
			return planner.ToolResult{}, fmt.Errorf("execute agent inline: %w", err)
		}

		return ConvertRunOutputToToolResult(call.Name, output), nil
	}
}
