// Package dsl declares agents and the toolsets they consume or export. Consuming
// Use expressions own loading choices; reusable tool contracts do not.
package dsl

import (
	"slices"

	expragents "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// Agent defines an LLM-based agent associated with the current service.
// A service may provide one or more agents. An agent consists of a system
// prompt, optional toolset dependencies, and a run policy. Agents can export
// toolsets for consumption by other agents or services.
//
// Agent must appear in a Service expression.
//
// Agent takes three arguments:
// - name: the name of the agent
// - description: a description of the agent
// - dsl: a function that defines the agent's system prompt, tools, and run policy
//
// The dsl function can use the following helpers:
// - Use / Export: declare the toolsets the agent consumes or exposes.
// - RunPolicy: defines the run policy for the agent.
//
// Example:
//
//	var DataIngestToolset = Toolset("data-ingest", func() {
//		Tool("csv-uploader", func() {
//			Args(func() { /* CSV file fields */ })
//			Return(func() { /* Validation result */ })
//		})
//	})
//
//	Agent("docs-agent", "Agent for managing documentation workflows", func() {
//		Use("summarization-tools", func() {
//			Tool("document-summarizer", "Summarize documents", func() {
//				Args(func() { /* Document fields */ })
//				Return(func() { /* Summary fields */ })
//			})
//		})
//		Use(DataIngestToolset)
//		Export("text-processing-suite", func() {
//			Tool("doc-abstractor", "Create document abstracts", func() {
//				Args(func() { /* Document fields */ })
//				Return(func() { /* Summary fields */ })
//			})
//		})
//		RunPolicy(func() {
//			DefaultCaps(MaxToolCalls(5), MaxRecoveryTurns(2))
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

// Use declares that the current agent consumes a toolset or registry.
// The value may be either:
//   - A *expragents.ToolsetExpr returned by Toolset (provider-owned)
//   - A string name for an inline, agent-local toolset definition
//   - A Registry expression, whose current service toolsets are resolved for
//     each planning activity
//
// An optional DSL function can:
//   - Subset tools from a referenced provider toolset by name (Tool("name"))
//   - Define ad-hoc tools local to this agent
//   - Defer all or selected tools through Deferred
//
// Example (referencing a provider toolset and subsetting):
//
//	var CommonTools = Toolset("common", func() {
//	    Tool("notify", "Send notification", func() { ... })
//	    Tool("log", "Log message", func() { ... })
//	})
//
//	Agent("assistant", "helper", func() {
//	    Use(CommonTools, func() {
//	        Tool("notify") // consume only a subset
//	    })
//	})
//
// Example (inline agent-local toolset):
//
//	Agent("planner", "Session planner", func() {
//	    Use("adhoc", func() {
//	        Tool("foo", "Foo tool", func() { ... })
//	    })
//	})
func Use(value any, fn ...func()) {
	agent, ok := eval.Current().(*expragents.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if len(fn) > 1 {
		eval.ReportError("Use accepts at most one DSL function")
		return
	}
	var dsl func()
	if len(fn) > 0 {
		dsl = fn[0]
	}
	if registry, ok := value.(*expragents.RegistryExpr); ok {
		if registry == nil {
			eval.ReportError("Use requires a registry")
			return
		}
		agent.Registries = append(agent.Registries, &expragents.RegistryUseExpr{
			Registry: registry, DSLFunc: dsl,
		})
		return
	}
	if agent.Used == nil {
		agent.Used = &expragents.ToolsetGroupExpr{Agent: agent}
	}
	ts := instantiateToolset(value, dsl, agent)
	if ts == nil {
		return
	}
	agent.Used.Toolsets = append(agent.Used.Toolsets, ts)
}

// Deferred makes tools in the surrounding Use declaration discoverable on
// demand. They remain permitted, but supporting model adapters load their full
// definitions only after tool search selects them. Adapters without tool-search
// support reject deferred requests. The choice belongs to this consuming agent,
// so other agents can use the same tools immediately.
//
// With no names, Deferred selects every tool in the consumed toolset or registry.
// Otherwise, names must exactly match local tool names in the compiled toolset;
// unlisted tools remain immediately available. Named selection is not supported
// for FromRegistry toolsets or whole registries because their tools are resolved
// at runtime. Empty, duplicate, unknown, and mixed all/named selections are errors.
//
// Deferred must appear inside a Use DSL function:
//
//	Use(Analytics, func() {
//	    Deferred("analyze", "forecast")
//	})
func Deferred(names ...string) {
	switch current := eval.Current().(type) {
	case *expragents.RegistryUseExpr:
		if len(names) > 0 {
			eval.ReportError("Deferred cannot select named tools from a Registry; use Deferred()")
			return
		}
		current.Deferred = true
	case *expragents.ToolsetExpr:
		if current.Agent == nil || current.Agent.Used == nil ||
			!slices.Contains(current.Agent.Used.Toolsets, current) {
			eval.ReportError("Deferred must appear inside a Use DSL function")
			return
		}
		if len(names) == 0 {
			if len(current.DeferredTools) > 0 {
				eval.ReportError("Deferred cannot mix all tools with named tools")
				return
			}
			current.Deferred = true
			return
		}
		if current.Provider != nil && current.Provider.Kind == expragents.ProviderRegistry {
			eval.ReportError("Deferred cannot select named tools from FromRegistry; use Deferred()")
			return
		}
		if current.Deferred {
			eval.ReportError("Deferred cannot mix all tools with named tools")
			return
		}
		for i, name := range names {
			if name == "" {
				eval.ReportError("Deferred tool name cannot be empty")
				return
			}
			if slices.Contains(current.DeferredTools, name) || slices.Contains(names[:i], name) {
				eval.ReportError("Deferred tool name %q is repeated", name)
				return
			}
		}
		current.DeferredTools = append(current.DeferredTools, names...)
	default:
		eval.IncompatibleDSL()
	}
}

// Export declares that the current agent or service exports the specified
// toolset for other agents to consume. Providers typically declare reusable
// toolsets at the top level via Toolset, then reference them from
// agents or services with Export.
func Export(value any, fn ...func()) *expragents.ToolsetExpr {
	if len(fn) > 1 {
		eval.ReportError("Export accepts at most one DSL function")
		return nil
	}
	var dsl func()
	if len(fn) > 0 {
		dsl = fn[0]
	}
	switch cur := eval.Current().(type) {
	case *expragents.AgentExpr:
		if cur.Exported == nil {
			cur.Exported = &expragents.ToolsetGroupExpr{Agent: cur}
		}
		ts := instantiateToolset(value, dsl, cur)
		if ts == nil {
			return nil
		}
		cur.Exported.Toolsets = append(cur.Exported.Toolsets, ts)
		return ts
	case *goaexpr.ServiceExpr:
		ts := instantiateToolset(value, dsl, nil)
		if ts == nil {
			return nil
		}
		se := ensureServiceExports(cur)
		se.Toolsets = append(se.Toolsets, ts)
		return ts
	default:
		eval.IncompatibleDSL()
		return nil
	}
}

// DisableAgentDocs disables generation of the AGENTS_QUICKSTART.md quickstart
// guide.
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

// Passthrough defines deterministic forwarding for an exported tool to a Goa
// service method. It must appear within the DSL of a Tool nested under
// Export.
//
// Passthrough accepts a tool name and a target, which can be:
//   - A *goaexpr.MethodExpr (e.g., Passthrough("tool", MyService.MyMethod))
//   - A service name and method name (e.g., Passthrough("tool", "MyService", "MyMethod"))
//
// Example:
//
//	Export("logging-tools", func() {
//	    Tool("log_message", "Log a message", func() {
//	        Args(func() { /* ... */ })
//	        Return(func() { /* ... */ })
//	        Passthrough("log_message", "LoggingService", "LogMessage")
//	    })
//	})
func Passthrough(toolName string, target any, methodNameOpt ...string) {
	tool, ok := eval.Current().(*expragents.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if tool.Name != toolName {
		eval.ReportError("Passthrough tool name %q does not match current tool %q", toolName, tool.Name)
		return
	}

	var serviceName, methodName string
	switch t := target.(type) {
	case *goaexpr.MethodExpr:
		if t.Service == nil {
			eval.ReportError("Passthrough target method must belong to a service")
			return
		}
		serviceName = t.Service.Name
		methodName = t.Name
	case string:
		serviceName = t
		if len(methodNameOpt) != 1 {
			eval.ReportError("Passthrough with service name requires a method name")
			return
		}
		methodName = methodNameOpt[0]
	default:
		eval.ReportError("Passthrough target must be a *goaexpr.MethodExpr or (serviceName string, methodName string)")
		return
	}

	if serviceName == "" || methodName == "" {
		eval.ReportError("Passthrough requires non-empty service and method names")
		return
	}

	tool.ExportPassthrough = &expragents.ToolPassthroughExpr{
		TargetService: serviceName,
		TargetMethod:  methodName,
	}
}

func ensureServiceExports(svc *goaexpr.ServiceExpr) *expragents.ServiceExportsExpr {
	for _, se := range expragents.Root.ServiceExports {
		if se.Service == svc {
			return se
		}
	}
	se := &expragents.ServiceExportsExpr{Service: svc}
	expragents.Root.ServiceExports = append(expragents.Root.ServiceExports, se)
	return se
}
