// Package dsl provides the Goa DSL functions for declaring agents, toolsets,
// prompts, completions, and MCP integrations.
//
// prompt.go defines the composable Prompt and SystemPrompt DSL surface. Prompts
// can be declared at the agent level (<service>.<agent>.<name>), service level
// (<service>.<name>), or method level (for MCP prompts/get integration).
// SystemPrompt provides dedicated syntax for agent system instructions with
// support for static text shorthand or parameterized closures with typed Args.
package dsl

import (
	agentsexpr "goa.design/goa-ai/expr/agent"
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// Prompt defines a prompt template or MCP method prompt depending on the current context.
//
// Context behavior:
//   - Inside an Agent expression (*agentsexpr.AgentExpr): declares an agent-owned prompt
//     with canonical ID `<service>.<agent>.<name>`. Defaults to user role.
//   - Inside a Service expression (*goaexpr.ServiceExpr): declares a service-level prompt
//     with canonical ID `<service>.<name>`, usable across agents and completions.
//     Requires a DSL closure.
//   - Inside a Method expression (*goaexpr.MethodExpr): preserves MCP method prompt
//     declaration for `prompts/get` protocol handling.
//
// Inside the Prompt DSL closure, authors can use:
//   - Role: sets the prompt role ("system", "user", "tool", "synthesis")
//   - Template: sets the Go text/template source string
//   - Description: human-readable metadata describing the prompt
//   - Args: defines the typed input argument schema available to the template
//
// Example (agent-scoped prompt with typed args):
//
//	Agent("assistant", "Support assistant", func() {
//	    Prompt("triage", func() {
//	        Description("Triage an incoming customer inquiry")
//	        Role("user")
//	        Template("Please triage this ticket: {{ .Inquiry.Body }} (Priority: {{ .Inquiry.Priority }})")
//	        Args(func() {
//	            Attribute("inquiry", TicketType, "Inquiry details")
//	            Required("inquiry")
//	        })
//	    })
//	})
//
// Example (service-level shared prompt):
//
//	Service("support", func() {
//	    Prompt("summarize_ticket", func() {
//	        Role("user")
//	        Template("Summarize the following conversation: {{ .Conversation }}")
//	        Args(func() {
//	            Attribute("conversation", String, "Conversation history")
//	            Required("conversation")
//	        })
//	    })
//	})
//
// Example (MCP method prompt):
//
//	Method("review", func() {
//	    Prompt("review", "Review source code for best practices")
//	})
func Prompt(name string, args ...any) {
	if name == "" {
		eval.ReportError("prompt name cannot be empty")
		return
	}

	switch parent := eval.Current().(type) {
	case *goaexpr.MethodExpr:
		svc := parent.Service
		var mcp *exprmcp.MCPExpr
		if r := exprmcp.Root; r != nil {
			mcp = r.GetMCP(svc)
		}
		if mcp == nil {
			eval.IncompatibleDSL()
			return
		}
		var description string
		if len(args) > 0 {
			if desc, ok := args[0].(string); ok {
				description = desc
			} else {
				eval.InvalidArgError("description string", args[0])
				return
			}
		}
		mcp.MethodPrompts = append(mcp.MethodPrompts, &exprmcp.MethodPromptExpr{
			Name:        name,
			Description: description,
			Method:      parent,
		})

	case *agentsexpr.AgentExpr:
		var description string
		var dslf func()
		for _, arg := range args {
			switch a := arg.(type) {
			case string:
				if description != "" {
					eval.ReportError("Prompt accepts at most one description string")
					return
				}
				description = a
			case func():
				if dslf != nil {
					eval.ReportError("Prompt accepts at most one DSL function")
					return
				}
				dslf = a
			default:
				eval.InvalidArgError("optional description string or DSL function", arg)
				return
			}
		}

		p := &agentsexpr.PromptExpr{
			Name:        name,
			Description: description,
			Role:        prompt.PromptRoleUser,
			Agent:       parent,
			Service:     parent.Service,
		}
		parent.Prompts = append(parent.Prompts, p)
		if dslf != nil {
			eval.Execute(dslf, p)
		}

	case *goaexpr.ServiceExpr:
		var description string
		var dslf func()
		for _, arg := range args {
			switch a := arg.(type) {
			case string:
				if description != "" {
					eval.ReportError("Prompt accepts at most one description string")
					return
				}
				description = a
			case func():
				if dslf != nil {
					eval.ReportError("Prompt accepts at most one DSL function")
					return
				}
				dslf = a
			default:
				eval.InvalidArgError("optional description string or DSL function", arg)
				return
			}
		}

		if dslf == nil {
			eval.IncompatibleDSL()
			return
		}

		p := &agentsexpr.PromptExpr{
			Name:        name,
			Description: description,
			Role:        prompt.PromptRoleUser,
			Service:     parent,
		}
		agentsexpr.Root.Prompts = append(agentsexpr.Root.Prompts, p)
		eval.Execute(dslf, p)

	default:
		eval.IncompatibleDSL()
		return
	}
}

// SystemPrompt defines the agent's baseline system prompt instructions.
//
// SystemPrompt must appear inside an Agent expression. It supports three call patterns:
//   - Static text shorthand: SystemPrompt("You are a helpful assistant.")
//   - Parameterized template with closure: SystemPrompt("You are {{ .AssistantName }}.", func() { ... })
//   - Full DSL closure: SystemPrompt(func() { Template("..."); Description("..."); Args(...) })
//
// SystemPrompt always evaluates as a PromptExpr with Name: "system" and Role: prompt.PromptRoleSystem.
//
// Example (static shorthand):
//
//	Agent("support_bot", "Customer support assistant", func() {
//	    SystemPrompt("You are a helpful customer support assistant.")
//	})
//
// Example (parameterized system prompt):
//
//	Agent("copilot", "Code assistant", func() {
//	    SystemPrompt("You are {{ .AssistantName }}. Style: {{ .StyleGuide }}", func() {
//	        Description("Baseline system instructions parameterized by tenant")
//	        Args(func() {
//	            Attribute("assistant_name", String, "Name of the assistant")
//	            Attribute("style_guide", String, "Coding style guidelines")
//	            Required("assistant_name", "style_guide")
//	        })
//	    })
//	})
func SystemPrompt(args ...any) {
	parent, ok := eval.Current().(*agentsexpr.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}

	if len(args) == 0 {
		eval.ReportError("SystemPrompt requires at least one argument")
		return
	}
	if len(args) > 2 {
		eval.TooManyArgError()
		return
	}

	var templateSource string
	var dslf func()

	if len(args) == 1 {
		switch a := args[0].(type) {
		case string:
			templateSource = a
		case func():
			dslf = a
		default:
			eval.InvalidArgError("template string or DSL function", args[0])
			return
		}
	} else {
		str, okStr := args[0].(string)
		fn, okFn := args[1].(func())
		if !okStr || !okFn {
			eval.InvalidArgError("template string followed by DSL function", args[0])
			return
		}
		templateSource = str
		dslf = fn
	}

	p := &agentsexpr.PromptExpr{
		Name:     "system",
		Role:     prompt.PromptRoleSystem,
		Template: templateSource,
		Agent:    parent,
		Service:  parent.Service,
	}
	parent.Prompts = append(parent.Prompts, p)
	if dslf != nil {
		eval.Execute(dslf, p)
	}
}

// Role defines the prompt role in model requests (system, user, tool, or synthesis).
//
// Role must appear inside a Prompt expression. It accepts either a prompt.PromptRole
// typed value or a string ("system", "user", "tool", "synthesis").
//
// Example:
//
//	Prompt("greeting", func() {
//	    Role("user")
//	    Template("Hello {{ .Name }}!")
//	})
func Role(role any) {
	p, ok := eval.Current().(*agentsexpr.PromptExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}

	switch r := role.(type) {
	case prompt.PromptRole:
		p.Role = r
	case string:
		switch r {
		case "system":
			p.Role = prompt.PromptRoleSystem
		case "user":
			p.Role = prompt.PromptRoleUser
		case "tool":
			p.Role = prompt.PromptRoleTool
		case "synthesis":
			p.Role = prompt.PromptRoleSynthesis
		default:
			eval.ReportError("invalid prompt role %q: must be one of \"system\", \"user\", \"tool\", or \"synthesis\"", r)
		}
	default:
		eval.InvalidArgError("prompt.PromptRole or string", role)
	}
}

// Template sets the baseline Go text/template source for a prompt.
//
// Template must appear inside a Prompt expression.
//
// Example:
//
//	Prompt("welcome", func() {
//	    Template("Welcome to {{ .WorkspaceName }}, {{ .User }}!")
//	})
func Template(tmpl string) {
	p, ok := eval.Current().(*agentsexpr.PromptExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	p.Template = tmpl
}
