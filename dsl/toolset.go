package dsl

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsexpr "goa.design/goa-ai/expr/agent"
)

// Toolset defines a provider-owned group of related tools. Declare toolsets at
// the top level using Toolset(...) and reference them from agents via
// Use / Export.
//
// Tools declared inside a Toolset may be:
//
//   - Bound to Goa service methods via BindTo, in which case codegen emits
//     transforms and client helpers.
//   - Backed by MCP tools declared with the MCP DSL (MCP + Tool) and
//     exposed via Toolset with FromMCP provider option.
//   - Implemented by custom executors or agent logic when left unbound.
//
// Toolset accepts a single form:
//
//   - Toolset("name", func()) declares a new toolset with the given name and tools.
//
// Example (provider toolset definition):
//
//	var CommonTools = Toolset("common", func() {
//	    Tool("notify", "Send notification", func() {
//	        Args(func() {
//	            Attribute("message", String, "Message to send")
//	            Required("message")
//	        })
//	    })
//	})
//
// Agents consume this toolset via Use:
//
//	Agent("assistant", "helper", func() {
//	    Use(CommonTools, func() {
//	        Tool("notify") // reference existing tool by name
//	    })
//	})
//
// For MCP-backed toolsets, use FromMCP provider option:
//
//	var MCPTools = Toolset(FromMCP("assistant-service", "assistant-mcp"))
//
// For registry-backed toolsets, use FromRegistry provider option:
//
//	var RegistryTools = Toolset(FromRegistry(CorpRegistry, "data-tools"))
//
// Toolset accepts these forms:
//   - Toolset("name", func()) - local toolset with inline schemas
//   - Toolset(FromMCP(service, toolset)) - MCP-backed toolset (name derived from toolset)
//   - Toolset(FromRegistry(registry, toolset)) - registry-backed toolset (name derived from toolset)
//   - Toolset("name", FromMCP(...)) - MCP-backed with explicit name
//   - Toolset("name", FromRegistry(...)) - registry-backed with explicit name
//   - Toolset(FromMCP(...), func()) - MCP-backed with additional config
func Toolset(args ...any) *agentsexpr.ToolsetExpr {
	if _, ok := eval.Current().(eval.TopExpr); !ok {
		eval.IncompatibleDSL()
		return nil
	}

	var name string
	var provider *agentsexpr.ProviderExpr
	var dsl func()

	for _, arg := range args {
		switch a := arg.(type) {
		case string:
			name = a
		case *agentsexpr.ProviderExpr:
			provider = a
		case func():
			dsl = a
		default:
			eval.InvalidArgError("name, provider option, or func()", arg)
			return nil
		}
	}

	// Derive name from provider if not explicitly set.
	if name == "" && provider != nil {
		switch provider.Kind {
		case agentsexpr.ProviderLocal:
			// Local providers require explicit name
		case agentsexpr.ProviderMCP:
			name = provider.MCPToolset
		case agentsexpr.ProviderRegistry:
			name = provider.ToolsetName
		case agentsexpr.ProviderA2A:
			name = provider.A2ASuite
		}
	}

	if name == "" {
		eval.ReportError("toolset name must be non-empty")
		return nil
	}

	ts := newToolsetDefinition(name, dsl)
	ts.Provider = provider

	agentsexpr.Root.Toolsets = append(agentsexpr.Root.Toolsets, ts)
	return ts
}

// FromMCP configures a toolset to be backed by an MCP server. Use FromMCP
// as a provider option when declaring a Toolset.
//
// FromMCP takes:
//   - service: Goa service name that owns the MCP server
//   - toolset: MCP server name (also used as the toolset name if not specified)
//
// Example:
//
//	var MCPTools = Toolset(FromMCP("assistant-service", "assistant-mcp"))
//
// Or with an explicit name:
//
//	var MCPTools = Toolset("my-tools", FromMCP("assistant-service", "assistant-mcp"))
func FromMCP(service, toolset string) *agentsexpr.ProviderExpr {
	if service == "" {
		eval.ReportError("FromMCP requires non-empty service name")
		return nil
	}
	if toolset == "" {
		eval.ReportError("FromMCP requires non-empty toolset name")
		return nil
	}
	return &agentsexpr.ProviderExpr{
		Kind:       agentsexpr.ProviderMCP,
		MCPService: service,
		MCPToolset: toolset,
	}
}

// FromRegistry configures a toolset to be sourced from a registry. Use
// FromRegistry as a provider option when declaring a Toolset.
//
// FromRegistry takes:
//   - registry: the RegistryExpr returned by Registry()
//   - toolset: name of the toolset in the registry (also used as the toolset name if not specified)
//
// Example:
//
//	var CorpRegistry = Registry("corp", func() {
//	    URL("https://registry.corp.internal")
//	})
//
//	var RegistryTools = Toolset(FromRegistry(CorpRegistry, "data-tools"))
//
// Or with an explicit name:
//
//	var RegistryTools = Toolset("my-tools", FromRegistry(CorpRegistry, "data-tools"))
//
// For version pinning, use the Version DSL inside the Toolset:
//
//	var PinnedTools = Toolset(FromRegistry(CorpRegistry, "data-tools"), func() {
//	    Version("1.2.3")
//	})
func FromRegistry(registry *agentsexpr.RegistryExpr, toolset string) *agentsexpr.ProviderExpr {
	if registry == nil {
		eval.ReportError("FromRegistry requires a non-nil registry")
		return nil
	}
	if toolset == "" {
		eval.ReportError("FromRegistry requires non-empty toolset name")
		return nil
	}
	return &agentsexpr.ProviderExpr{
		Kind:        agentsexpr.ProviderRegistry,
		Registry:    registry,
		ToolsetName: toolset,
	}
}

// FromA2A configures a toolset to be backed by a remote A2A provider. Use
// FromA2A as a provider option when declaring a Toolset.
//
// FromA2A takes:
//   - suite: A2A suite identifier for the remote agent (for example, "svc.agent.tools")
//   - url:   base URL for the remote A2A server
//
// Example:
//
//	var RemoteTools = Toolset(FromA2A("svc.agent.tools", "https://provider.example.com"))
//
// Or with an explicit name:
//
//	var RemoteTools = Toolset("remote-tools", FromA2A("svc.agent.tools", "https://provider.example.com"))
func FromA2A(suite, url string) *agentsexpr.ProviderExpr {
	if suite == "" {
		eval.ReportError("FromA2A requires non-empty suite identifier")
		return nil
	}
	if url == "" {
		eval.ReportError("FromA2A requires non-empty URL")
		return nil
	}
	return &agentsexpr.ProviderExpr{
		Kind:     agentsexpr.ProviderA2A,
		A2ASuite: suite,
		A2AURL:   url,
	}
}

// buildToolsetExpr constructs a ToolsetExpr from a value and DSL function.
func newToolsetDefinition(name string, dsl func()) *agentsexpr.ToolsetExpr {
	return &agentsexpr.ToolsetExpr{
		Name:    name,
		DSLFunc: dsl,
	}
}

func cloneToolset(origin *agentsexpr.ToolsetExpr, agent *agentsexpr.AgentExpr, overlay func()) *agentsexpr.ToolsetExpr {
	if origin == nil {
		eval.ReportError("toolset reference cannot be nil")
		return nil
	}
	dup := &agentsexpr.ToolsetExpr{
		Name:        origin.Name,
		Description: origin.Description,
		Tags:        append([]string(nil), origin.Tags...),
		Agent:       agent,
		Provider:    origin.Provider,
		Origin:      origin,
	}
	switch {
	case origin.DSLFunc != nil && overlay != nil:
		dup.DSLFunc = func() {
			origin.DSLFunc()
			overlay()
		}
	case overlay != nil:
		dup.DSLFunc = overlay
	default:
		dup.DSLFunc = origin.DSLFunc
	}
	return dup
}

func instantiateToolset(value any, overlay func(), agent *agentsexpr.AgentExpr) *agentsexpr.ToolsetExpr {
	switch v := value.(type) {
	case string:
		if v == "" {
			eval.ReportError("toolset name must be non-empty")
			return nil
		}
		return &agentsexpr.ToolsetExpr{
			Name:    v,
			DSLFunc: overlay,
			Agent:   agent,
		}
	case *agentsexpr.ToolsetExpr:
		return cloneToolset(v, agent, overlay)
	default:
		eval.ReportError("toolset must be referenced by name or Toolset expression")
		return nil
	}
}

// AgentToolset references a toolset exported by another agent identified by
// service and agent names. Use inside an Agent's Uses block to explicitly
// depend on an exported toolset when inference is not possible or ambiguous.
//
// When to use AgentToolset vs Toolset:
//   - Prefer Toolset(X) when you already have an expression handle (e.g., a
//     top-level Toolset variable or an agent's exported Toolset). Goa-AI will
//     infer a RemoteAgent provider automatically when exactly one agent in a
//     different service Exports a toolset with the same name.
//   - Use AgentToolset(service, agent, toolset) when you:
//   - Do not have an expression handle to the exported toolset, or
//   - Have ambiguity (multiple agents export a toolset with the same name), or
//   - Want to be explicit in the design for clarity.
//
// AgentToolset(service, agent, toolset)
//   - service: Goa service name that owns the exporting agent
//   - agent:   Agent name in that service
//   - toolset: Exported toolset name in that agent
//
// The referenced toolset is resolved from the design, and a local reference is
// recorded with its Origin set to the defining toolset. Provider information is
// inferred during validation and will classify this as a RemoteAgent provider
// when the owner service differs from the consumer service.
func AgentToolset(service, agent, toolset string) *agentsexpr.ToolsetExpr {
	if service == "" || agent == "" || toolset == "" {
		eval.ReportError("AgentToolset requires non-empty service, agent, and toolset")
		return nil
	}
	svc := goaexpr.Root.Service(service)
	if svc == nil {
		eval.ReportError("AgentToolset could not resolve service %q", service)
		return nil
	}
	var originAgent *agentsexpr.AgentExpr
	for _, a := range agentsexpr.Root.Agents {
		if a != nil && a.Service == svc && a.Name == agent {
			originAgent = a
			break
		}
	}
	if originAgent == nil || originAgent.Exported == nil {
		eval.ReportError("AgentToolset could not find exported toolsets for %q.%q", service, agent)
		return nil
	}
	for _, ts := range originAgent.Exported.Toolsets {
		if ts != nil && ts.Name == toolset {
			return ts
		}
	}
	eval.ReportError("AgentToolset could not resolve toolset %q exported by agent %q.%q", toolset, service, agent)
	return nil
}

// UseAgentToolset is an alias for AgentToolset. Prefer AgentToolset in new
// designs; this alias exists for readability in some codebases.
//
// Deprecated: Use AgentToolset instead. This function will be removed in a
// future release.
func UseAgentToolset(service, agent, toolset string) *agentsexpr.ToolsetExpr {
	ts := AgentToolset(service, agent, toolset)
	if ts == nil {
		return nil
	}
	return Use(ts)
}
