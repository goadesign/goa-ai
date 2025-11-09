package agent

import (
	"fmt"
	"strings"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// ToolsetExpr captures a toolset declaration from the agent DSL.
	ToolsetExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this toolset.
		Name string
		// Description provides a human-readable explanation of the
		// toolset's purpose.
		Description string
		// Tags are labels for categorizing and filtering this toolset.
		Tags []string
		// Agent is the agent expression that owns this toolset, if any.
		Agent *AgentExpr
		// Tools is the collection of tool expressions in this toolset.
		Tools []*ToolExpr
		// External indicates whether this toolset is provided by an
		// external MCP server.
		External bool
		// MCPService is the Goa service name for the MCP client, if
		// this is an external toolset.
		MCPService string
		// MCPSuite is the MCP suite identifier for grouping external
		// toolsets.
		MCPSuite string

		// Origin references the original defining toolset when this toolset
		// is a reference/alias (e.g., consumed under Uses or via AgentToolset).
		// When nil, this toolset is the defining origin.
		Origin *ToolsetExpr

		// Provider describes where and how this toolset is executed. It is
		// inferred during validation from the DSL structure:
		//   - Local: tools execute within the current service
		//   - RemoteAgent: tools are exported by another service's agent
		//   - MCP: tools are provided by an MCP server
		Provider ProviderInfo
	}
)

// ProviderKind enumerates where a toolset executes.
type ProviderKind string

const (
	// ProviderLocal indicates tools execute within the current service/process.
	ProviderLocal ProviderKind = "local"
	// ProviderRemoteAgent indicates tools are executed by another service's agent.
	ProviderRemoteAgent ProviderKind = "remote_agent"
	// ProviderMCP indicates tools are provided by an MCP server.
	ProviderMCP ProviderKind = "mcp"
)

// ProviderInfo describes the execution provider for a toolset.
//
// When Kind is ProviderLocal, ServiceName is typically the current service.
// When Kind is ProviderRemoteAgent, ServiceName/AgentName/ToolsetName identify
// the origin that owns execution. When Kind is ProviderMCP, ServiceName is the
// MCP Go service (client) name and ToolsetName is the MCP suite.
type ProviderInfo struct {
	// Kind identifies the provider classification (local, remote_agent, mcp).
	Kind ProviderKind
	// ServiceName is the owning service (Goa service) for the provider.
	ServiceName string
	// AgentName is set when Kind == ProviderRemoteAgent.
	AgentName string
	// ToolsetName is the logical toolset identifier at the provider side.
	ToolsetName string
}

// EvalName is part of eval.Expression allowing descriptive error messages.
func (t *ToolsetExpr) EvalName() string {
	return fmt.Sprintf("toolset %q", t.Name)
}

// WalkSets exposes the nested expressions to the eval engine.
func (t *ToolsetExpr) WalkSets(walk eval.SetWalker) {
	walk(eval.ToExpressionSet(t.Tools))
}

// Validate performs semantic checks on the toolset expression.
func (t *ToolsetExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if t.External {
		if strings.TrimSpace(t.MCPSuite) == "" {
			verr.Add(t, "MCP suite name is required; set it via MCP(\"<suite>\", ...) block name")
		}
		if strings.TrimSpace(t.MCPService) != "" {
			if goaexpr.Root.Service(t.MCPService) == nil {
				verr.Add(t, "MCP FromService could not resolve service %q", t.MCPService)
			}
		}
	}
	// Infer provider information. Prefer MCP classification when External is set.
	switch {
	case t.External:
		t.Provider.Kind = ProviderMCP
		t.Provider.ServiceName = t.MCPService
		t.Provider.ToolsetName = t.MCPSuite
		t.Provider.AgentName = ""
	default:
		// Start with local by default.
		t.Provider.Kind = ProviderLocal
		if t.Agent != nil && t.Agent.Service != nil {
			t.Provider.ServiceName = t.Agent.Service.Name
		}
		t.Provider.ToolsetName = t.Name
		// If this is a reference to an exported toolset owned by another service,
		// classify as remote agent and record the origin details.
		if t.Origin != nil {
			if t.Origin.Agent != nil && t.Agent != nil && t.Agent.Service != nil && t.Origin.Agent.Service != nil {
				if t.Origin.Agent.Service.Name != t.Agent.Service.Name {
					t.Provider.Kind = ProviderRemoteAgent
					t.Provider.ServiceName = t.Origin.Agent.Service.Name
					t.Provider.AgentName = t.Origin.Agent.Name
					t.Provider.ToolsetName = t.Origin.Name
				}
			} else if t.Agent != nil && t.Agent.Service != nil {
				// Origin exists but has no owning agent (top-level Toolset reference).
				// Attempt to resolve the provider export with the same toolset name.
				var match *ToolsetExpr
				var matchAgent *AgentExpr
				for _, a := range Root.Agents {
					if a == nil || a.Exported == nil || a.Service == nil {
						continue
					}
					if a.Service.Name == t.Agent.Service.Name {
						continue
					}
					for _, ets := range a.Exported.Toolsets {
						if ets != nil && ets.Name == t.Name {
							if match != nil {
								match = nil
								matchAgent = nil
								goto found_origin_done
							}
							match = ets
							matchAgent = a
						}
					}
				}
			found_origin_done:
				if match != nil && matchAgent != nil {
					t.Origin = match
					t.Provider.Kind = ProviderRemoteAgent
					t.Provider.ServiceName = matchAgent.Service.Name
					t.Provider.AgentName = matchAgent.Name
					t.Provider.ToolsetName = match.Name
				}
			}
		}
		// If no origin attached (e.g., referencing a top-level Toolset expression),
		// attempt to infer a remote provider by finding a single exported toolset
		// with the same name in another service. Ambiguity (0 or >1 matches) leaves
		// the provider as Local so designs can disambiguate via AgentToolset.
		if t.Provider.Kind == ProviderLocal && t.Origin == nil && t.Agent != nil && t.Agent.Service != nil {
			var match *ToolsetExpr
			var matchAgent *AgentExpr
			for _, a := range Root.Agents {
				if a == nil || a.Exported == nil || a.Service == nil {
					continue
				}
				if a.Service.Name == t.Agent.Service.Name {
					continue
				}
				for _, ets := range a.Exported.Toolsets {
					if ets != nil && ets.Name == t.Name {
						if match != nil {
							match = nil
							matchAgent = nil
							goto inference_done
						}
						match = ets
						matchAgent = a
					}
				}
			}
		inference_done:
			if match != nil && matchAgent != nil {
				t.Origin = match
				t.Provider.Kind = ProviderRemoteAgent
				t.Provider.ServiceName = matchAgent.Service.Name
				t.Provider.AgentName = matchAgent.Name
				t.Provider.ToolsetName = match.Name
			}
		}
	}
	return verr
}
