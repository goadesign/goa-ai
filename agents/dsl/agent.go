package dsl

import (
	"goa.design/goa-ai/agents/expr"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
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
//		var DataIngestToolset = Toolset("data-ingest", func() {
//			Tool("csv-uploader", func() {
//				Input(func() { /* CSV file fields */ })
//				Output(func() { /* Validation result */ })
//			})
//		})
//
//		Agent("docs-agent", "Agent for managing documentation workflows", func() {
//			SystemPrompt("You are a helpful assistant that can summarize documents.")
//			Uses(func() {
//				Toolset("summarization-tools", func() {
//					Tool("document-summarizer", func() {
//						Input(func() { /* Document fields */ })
//						Output(func() { /* Summary fields */ })
//					})
//				})
//	            Toolset(DataIngestToolset)
//		    })
//			Exports(func() {
//				Toolset("text-processing-suite", func() {
//					Tool("doc-abstractor", func() {
//						Input(func() { /* Document fields */ })
//						Output(func() { /* Summary fields */ })
//					})
//				})
//			})
//			RunPolicy(func() {
//				DefaultCaps(MaxToolCalls(5), MaxConsecutiveFailedToolCalls(2))
//				TimeBudget("30s")
//			})
//		})
func Agent(name, description string, dsl func()) *expr.AgentExpr {
	if name == "" {
		eval.ReportError("agent name cannot be empty")
		return nil
	}
	svc, ok := eval.Current().(*goaexpr.ServiceExpr)
	if !ok {
		eval.IncompatibleDSL()
		return nil
	}
	agent := &expr.AgentExpr{
		Name:        name,
		Description: description,
		Service:     svc,
		DSLFunc:     dsl,
	}
	expr.Root.Agents = append(expr.Root.Agents, agent)
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
	agent, ok := eval.Current().(*expr.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	agent.Used = &expr.ToolsetGroupExpr{Agent: agent, DSLFunc: fn}
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
	agent, ok := eval.Current().(*expr.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	agent.Exported = &expr.ToolsetGroupExpr{Agent: agent, DSLFunc: fn}
}
