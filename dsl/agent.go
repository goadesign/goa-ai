// Package dsl provides the Goa DSL functions for declaring agents, toolsets,
// and related runtime policies. These functions are evaluated during design
// parsing and populate the expr package types used for code generation.
package dsl

import (
	expragents "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	// Import codegen package to ensure agent code generation plugin is registered
	_ "goa.design/goa-ai/codegen/agent"
)

// Agent defines an LLM-based agent associated with the current service.
// A service may provide one or more agents. An agent consists of a system prompt,
// optional toolset dependencies, and a run policy. Agents can export toolsets
// for consumption by other agents.
//
// Agent must appear in a Service expression.
//
// Agent takes three arguments:
// - name: the name of the agent
// - description: a description of the agent
// - dsl: a function that defines the agent's system prompt, tools, and run policy
//
// The dsl function can use the following functions to define the agent:
// - Toolset: defines a toolset that the agent can use
// - Tool: defines a tool that the agent can use
// - RunPolicy: defines the run policy for the agent
//
// Example:
//
//	var DataIngestToolset = Toolset("data-ingest", func() {
//		Tool("csv-uploader", func() {
//			Input(func() { /* CSV file fields */ })
//			Output(func() { /* Validation result */ })
//		})
//	})
//
//	Agent("docs-agent", "Agent for managing documentation workflows", func() {
//		Uses(func() {
//			Toolset("summarization-tools", func() {
//				Tool("document-summarizer", func() {
//					Input(func() { /* Document fields */ })
//					Output(func() { /* Summary fields */ })
//				})
//			})
//	            Toolset(DataIngestToolset)
//	    })
//		Exports(func() {
//			Toolset("text-processing-suite", func() {
//				Tool("doc-abstractor", func() {
//					Input(func() { /* Document fields */ })
//					Output(func() { /* Summary fields */ })
//				})
//			})
//		})
//		RunPolicy(func() {
//			DefaultCaps(MaxToolCalls(5), MaxConsecutiveFailedToolCalls(2))
//			TimeBudget("30s")
//		})
//	})
func Agent(name, description string, dsl func()) *expragents.AgentExpr {
	if name == "" {
		eval.ReportError("agent name cannot be empty")
		return nil
	}
	svc, ok := eval.Current().(*goaexpr.ServiceExpr)
	if !ok {
		eval.IncompatibleDSL()
		return nil
	}
	agent := &expragents.AgentExpr{
		Name:        name,
		Description: description,
		Service:     svc,
		DSLFunc:     dsl,
	}
	expragents.Root.Agents = append(expragents.Root.Agents, agent)
	return agent
}

// Uses declares the toolsets that the current agent consumes. Toolsets may be
// declared inline or referenced from existing toolset definitions.
//
// Uses must appear in an Agent expression.
//
// Uses takes a single argument which is the defining DSL function.
//
// The DSL function may contain:
//   - Toolset declarations (inline or by reference)
//   - MCPToolset declarations for external MCP servers
//
// Example:
//
//	Agent("docs-agent", "Document processor", func() {
//	    Uses(func() {
//	        Toolset("summarization", func() {
//	            Tool("summarizer", "Summarize text", func() { ... })
//	        })
//	        Toolset(SharedToolset)  // Reference existing toolset
//	        MCPToolset("external", "search")  // External MCP server
//	    })
//	})
func Uses(fn func()) {
	agent, ok := eval.Current().(*expragents.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	agent.Used = &expragents.ToolsetGroupExpr{Agent: agent, DSLFunc: fn}
}

// Exports declares the toolsets that the current agent provides for other
// agents to consume. Exported toolsets define the agent's public tool API.
//
// Exports must appear in an Agent expression.
//
// Exports takes a single argument which is the defining DSL function.
//
// The DSL function may contain:
//   - Toolset declarations (inline only, not references)
//
// Example:
//
//	Agent("docs-agent", "Document processor", func() {
//	    Exports(func() {
//	        Toolset("document-tools", func() {
//	            Tool("summarize", "Summarize document", func() { ... })
//	            Tool("extract", "Extract metadata", func() { ... })
//	        })
//	    })
//	})
func Exports(fn func()) {
	agent, ok := eval.Current().(*expragents.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	agent.Exported = &expragents.ToolsetGroupExpr{Agent: agent, DSLFunc: fn}
}

// DisableAgentDocs disables generation of the AGENTS_QUICKSTART.md quickstart guide.
//
// Call DisableAgentDocs() inside your API design to opt-out of generating the
// contextual agent quickstart README at the module root. This affects only the
// documentation artifact; all other generated code is unaffected.
//
// Example:
//
//	var _ = API("assistant", func() {
//	    // ...
//	    DisableAgentDocs()
//	})
func DisableAgentDocs() {
	expragents.Root.DisableAgentDocs = true
}
