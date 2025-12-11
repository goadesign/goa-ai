package agent

import "fmt"

// ProviderKind identifies the source/executor type for a toolset.
type ProviderKind int

const (
	// ProviderLocal indicates a toolset with inline schemas defined
	// directly in the DSL.
	ProviderLocal ProviderKind = iota
	// ProviderMCP indicates a toolset backed by an MCP server.
	ProviderMCP
	// ProviderRegistry indicates a toolset sourced from a registry.
	ProviderRegistry
)

// String returns a human-readable representation of the provider kind.
func (k ProviderKind) String() string {
	switch k {
	case ProviderLocal:
		return "local"
	case ProviderMCP:
		return "mcp"
	case ProviderRegistry:
		return "registry"
	default:
		return fmt.Sprintf("unknown(%d)", k)
	}
}

// ProviderExpr captures the provider configuration for a toolset,
// specifying where tool schemas come from and how tools are executed.
type ProviderExpr struct {
	// Kind identifies the provider type (local, MCP, registry).
	Kind ProviderKind
	// MCPService is the Goa service name that owns the MCP server
	// definition. Used when Kind is ProviderMCP.
	MCPService string
	// MCPToolset is the MCP server name for this toolset. Used when
	// Kind is ProviderMCP.
	MCPToolset string
	// Registry references the registry source for this toolset.
	// Used when Kind is ProviderRegistry.
	Registry *RegistryExpr
	// ToolsetName is the name of the toolset in the registry.
	// Used when Kind is ProviderRegistry.
	ToolsetName string
	// Version pins the toolset to a specific version.
	// Used when Kind is ProviderRegistry.
	Version string
}

// EvalName returns a descriptive identifier for error reporting.
func (p *ProviderExpr) EvalName() string {
	switch p.Kind {
	case ProviderLocal:
		return "local provider"
	case ProviderMCP:
		return fmt.Sprintf("MCP provider (service=%q, toolset=%q)", p.MCPService, p.MCPToolset)
	case ProviderRegistry:
		regName := ""
		if p.Registry != nil {
			regName = p.Registry.Name
		}
		return fmt.Sprintf("registry provider (registry=%q, toolset=%q)", regName, p.ToolsetName)
	default:
		return "local provider"
	}
}
