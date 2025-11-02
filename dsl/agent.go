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
//		SystemPrompt("You are a helpful assistant that can summarize documents.")
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

// Uses declares the toolsets consumed by the current agent. Toolsets
// can be declared inline or by reference.
//
// Example usage:
//
//	Agent("docs-agent", "Agent description", func() {
//	    Uses(func() {
//	        Toolset("summarization-tools", func() {
//	            Tool("summarizer", func() { ... })
//	        })
//	        Toolset(OtherToolsetReference)
//	    })
//	})
//
// The function passed to Uses should declare Toolsets to be used by the agent.
func Uses(fn func()) {
	agent, ok := eval.Current().(*expragents.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	agent.Used = &expragents.ToolsetGroupExpr{Agent: agent, DSLFunc: fn}
}

// Exports declares toolsets exposed by the current agent.
// Toolsets can be declared inline or by reference.
//
// Example usage:
//
//	Agent("docs-agent", "Agent description", func() {
//	    Exports(func() {
//	        Toolset("summarization-tools", func() {
//	            Tool("summarizer", func() { ... })
//	        })
//	        Toolset(OtherToolsetReference)
//	    })
//	})
//
// The function passed to Exports should declare Toolsets to be exported by the agent.
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
