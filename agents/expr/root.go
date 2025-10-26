package expr

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// RootExpr represents the top-level root for all agent and toolset declarations
// captured during a Goa design run. It is registered with the evaluation engine
// and provides the anchor point for collecting and organizing agents and toolsets
// for code generation, validation, and dependency resolution.
//
// The root is automatically populated during DSL evaluation as Agent and Toolset
// expressions are declared. WalkSets ensures proper evaluation order: agents first,
// then toolset groups, then individual toolsets, and finally tools.
type RootExpr struct {
	// Agents is the complete list of agents declared across all services in the design.
	Agents []*AgentExpr
	// Toolsets is the complete list of all toolsets, including global toolsets and those
	// referenced by agents via Uses or Exports blocks.
	Toolsets []*ToolsetExpr
}

// Root holds all agent DSL declarations for the current Goa design run.
var Root *RootExpr

// init registers the root expression with the eval engine.
func init() {
	Root = &RootExpr{}
	if err := eval.Register(Root); err != nil {
		panic(err)
	}
}

// EvalName is part of eval.Expression.
func (r *RootExpr) EvalName() string {
	return "agents root"
}

// DependsOn returns the Goa roots this plugin depends on.
func (r *RootExpr) DependsOn() []eval.Root {
	return []eval.Root{goaexpr.Root}
}

// Packages returns packages considered for DSL error attribution.
func (r *RootExpr) Packages() []string {
	return []string{
		"goa.design/goa-ai/agents/dsl",
	}
}

// WalkSets exposes the nested expressions to the eval engine.
func (r *RootExpr) WalkSets(walk eval.SetWalker) {
	// Execute agent DSLs first so Uses/Exports blocks register toolset groups.
	walk(eval.ToExpressionSet(r.Agents))

	// Execute toolset group DSLs (Uses/Exports) for each agent.
	var groups eval.ExpressionSet
	for _, agent := range r.Agents {
		if agent.Used != nil {
			groups = append(groups, agent.Used)
		}
		if agent.Exported != nil {
			groups = append(groups, agent.Exported)
		}
	}
	if len(groups) > 0 {
		walk(groups)
	}

	// Execute toolset DSLs (global or per-agent) now that groups populated them.
	var toolsets []*ToolsetExpr
	for _, agent := range r.Agents {
		if agent.Used != nil {
			toolsets = append(toolsets, agent.Used.Toolsets...)
		}
		if agent.Exported != nil {
			toolsets = append(toolsets, agent.Exported.Toolsets...)
		}
	}
	toolsets = append(toolsets, r.Toolsets...)
	if len(toolsets) > 0 {
		walk(eval.ToExpressionSet(toolsets))
	}

	// Execute tool DSLs defined within each toolset.
	var tools []*ToolExpr
	for _, ts := range toolsets {
		tools = append(tools, ts.Tools...)
	}
	if len(tools) > 0 {
		walk(eval.ToExpressionSet(tools))
	}
}
