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
		if strings.TrimSpace(t.MCPSuite) == "" {
			verr.Add(t, "MCP suite name is required; set it via MCP(\"<suite>\", ...) block name")
		}
		if strings.TrimSpace(t.MCPService) != "" {
			if goaexpr.Root.Service(t.MCPService) == nil {
				verr.Add(t, "MCP FromService could not resolve service %q", t.MCPService)
			}
		}
	}
	return verr
}
