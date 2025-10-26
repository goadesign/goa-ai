package expr

import (
	"fmt"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// AgentExpr describes a single LLM-powered agent configured via the Goa Agent DSL.
	//
	// Each AgentExpr captures the agent's name, description, the parent Goa service,
	// all used and exported toolsets, and resource/running policies. Instances are
	// built during DSL execution and form the core agent schema for configuration,
	// capability wiring, and runtime policy enforcement.
	//
	// Every AgentExpr is always attached to a specific Goa service (ServiceExpr),
	// and both Used and Exported groups are guaranteed non-nil after Prepare. The
	// eval engine walks the nested toolset groups and tools during evaluation.
	AgentExpr struct {
		eval.DSLFunc

		// Name is the agent's unique identifier within its service.
		Name string
		// Description is a short summary of the agent's purpose and capabilities.
		Description string
		// Service is the Goa service that declares this agent (always non-nil).
		Service *goaexpr.ServiceExpr

		// Used is the group of toolsets this agent consumes (may be nil before DSL execution).
		Used *ToolsetGroupExpr
		// Exported is the group of toolsets this agent exposes to other agents (may be nil before DSL execution).
		Exported *ToolsetGroupExpr

		// RunPolicy defines resource limits and execution constraints (guaranteed non-nil after Prepare).
		RunPolicy *RunPolicyExpr
	}

	// ToolsetGroupExpr represents a logical group of toolsets, as exposed or consumed by an agent.
	//
	// Instances are created during DSL execution when Uses or Exports blocks are declared.
	// The Agent field always points to the owning agent, and Toolsets accumulates the
	// toolset expressions declared within the group's DSL block.
	ToolsetGroupExpr struct {
		eval.DSLFunc

		// Agent is the owning agent (always non-nil).
		Agent *AgentExpr
		// Toolsets is the list of toolsets in this group (accumulated during DSL execution).
		Toolsets []*ToolsetExpr
	}
)

// EvalName is part of eval.Expression allowing descriptive error messages.
func (a *AgentExpr) EvalName() string {
	return fmt.Sprintf("agent %q (service %q)", a.Name, a.Service.Name)
}

// WalkSets exposes the nested expressions to the eval engine.
func (a *AgentExpr) WalkSets(walk eval.SetWalker) {
	if a.Used != nil {
		walk(eval.ExpressionSet{a.Used})
		walk(eval.ToExpressionSet(a.Used.Toolsets))
	}
	if a.Exported != nil {
		walk(eval.ExpressionSet{a.Exported})
		walk(eval.ToExpressionSet(a.Exported.Toolsets))
	}
}

// Prepare ensures there is run policy.
func (a *AgentExpr) Prepare() {
	if a.RunPolicy == nil {
		a.RunPolicy = &RunPolicyExpr{Agent: a}
	}
}

// EvalName is part of eval.Expression allowing descriptive error messages.
func (t *ToolsetGroupExpr) EvalName() string {
	return fmt.Sprintf("toolset group for agent %q", t.Agent.Name)
}
