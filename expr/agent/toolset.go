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
	}
)

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
		if t.MCPSuite == "" {
			verr.Add(t, "MCP suite name is required; set it via MCP(\"<suite>\", ...) block name")
		}
		if t.MCPService != "" {
			if goaexpr.Root.Service(t.MCPService) == nil {
				verr.Add(t, "MCP FromService could not resolve service %q", t.MCPService)
			}
		}
	}
	return verr
}
