package agent

import (
	"fmt"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// ToolsetExpr captures a toolset declaration from the agent DSL.
	ToolsetExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this toolset within the design.
		// Root-level validation enforces that defining toolsets (Origin == nil)
		// use globally unique names so tooling can treat toolset IDs as simple
		// names.
		Name string

		// Description provides a human-readable explanation of the
		// toolset's purpose.
		Description string

		// Tags are labels for categorizing and filtering this toolset.
		Tags []string

		// Agent is the agent expression that owns this toolset, if any.
		// When nil, the toolset is either top-level or attached to a
		// service export.
		Agent *AgentExpr

		// Tools is the collection of tool expressions in this toolset.
		Tools []*ToolExpr

		// Provider configures the source/executor for this toolset.
		// When nil, the toolset is local with inline schemas.
		Provider *ProviderExpr

		// PublishTo specifies registries where this toolset should be
		// published when exported.
		PublishTo []*RegistryExpr

		// Origin references the original defining toolset when this toolset
		// is a reference/alias (e.g., consumed under Uses or via AgentToolset).
		// When nil, this toolset is the defining origin.
		Origin *ToolsetExpr

		// A2A holds optional A2A configuration for this toolset when it is
		// exported via the A2A protocol. It is populated by the A2A DSL
		// helper and validated at the root level to ensure it only appears
		// on exported toolsets.
		A2A *A2AExpr
	}
)

// EvalName is part of eval.Expression allowing descriptive error messages.
func (t *ToolsetExpr) EvalName() string {
	return fmt.Sprintf("toolset %q", t.Name)
}

// SetDescription implements expr.DescriptionHolder, allowing the Description()
// DSL function to set the toolset description.
func (t *ToolsetExpr) SetDescription(d string) {
	t.Description = d
}

// SetVersion implements expr.VersionHolder, allowing the Version() DSL
// function to set the toolset version. Version is only valid for
// registry-backed toolsets.
func (t *ToolsetExpr) SetVersion(v string) {
	if t.Provider == nil || t.Provider.Kind != ProviderRegistry {
		// Validation will catch this; just store it for now
		if t.Provider == nil {
			t.Provider = &ProviderExpr{}
		}
	}
	t.Provider.Version = v
}

// WalkSets exposes the nested expressions to the eval engine.
func (t *ToolsetExpr) WalkSets(walk eval.SetWalker) {
	walk(eval.ToExpressionSet(t.Tools))
	if t.A2A != nil {
		walk(eval.ExpressionSet{t.A2A})
	}
}

// Validate performs semantic checks on the toolset expression.
func (t *ToolsetExpr) Validate() error {
	verr := new(eval.ValidationErrors)

	// Validate provider configuration.
	if t.Provider != nil {
		switch t.Provider.Kind {
		case ProviderMCP:
			if t.Provider.MCPToolset == "" {
				verr.Add(t, "MCP server name is required; set it via FromMCP(service, toolset)")
			}
			if t.Provider.MCPService != "" {
				if goaexpr.Root.Service(t.Provider.MCPService) == nil {
					verr.Add(t, "FromMCP could not resolve service %q", t.Provider.MCPService)
				}
			}
		case ProviderRegistry:
			if t.Provider.Registry == nil {
				verr.Add(t, "registry is required for FromRegistry provider")
			}
			if t.Provider.ToolsetName == "" {
				verr.Add(t, "toolset name is required for FromRegistry provider")
			}
		case ProviderA2A:
			if t.Provider.A2ASuite == "" {
				verr.Add(t, "A2A suite is required; set it via FromA2A(suite, url)")
			}
			if t.Provider.A2AURL == "" {
				verr.Add(t, "A2A URL is required; set it via FromA2A(suite, url)")
			}
		case ProviderLocal:
			// Local toolsets have inline schemas; no additional validation needed.
		}
	}

	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}
