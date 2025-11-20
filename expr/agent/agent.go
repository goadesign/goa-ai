// Package agent defines the expression types used to represent agent and toolset
// declarations during Goa design evaluation. These types are populated during
// DSL execution and form the schema used for code generation and validation.
package agent

import (
	"fmt"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// AgentExpr describes a single LLM-powered agent configured via the Goa Agent DSL.
	AgentExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this agent.
		Name string
		// Description provides a human-readable explanation of the
		// agent's purpose and capabilities.
		Description string
		// Service is the Goa service expression this agent is
		// associated with.
		Service *goaexpr.ServiceExpr
		// Used contains the toolsets this agent consumes from other
		// agents or services.
		Used *ToolsetGroupExpr
		// Exported contains the toolsets this agent exposes for other
		// agents to consume.
		Exported *ToolsetGroupExpr
		// RunPolicy defines runtime execution and resource constraints
		// for this agent.
		RunPolicy *RunPolicyExpr
		// Methods contains the routing strategies for agent methods.
		Methods []*AgentMethodExpr
	}

	// ToolsetGroupExpr represents a logical group of toolsets, as exposed
	// or consumed by an agent.
	ToolsetGroupExpr struct {
		eval.DSLFunc

		// Agent is the agent expression that owns this toolset group.
		Agent *AgentExpr
		// Toolsets is the collection of toolset expressions in this
		// group.
		Toolsets []*ToolsetExpr
	}

	// ServiceExportsExpr represents toolsets exported directly by a
	// Goa service via a Service-level Exports block.
	ServiceExportsExpr struct {
		eval.DSLFunc

		// Service is the Goa service that owns these exported toolsets.
		Service *goaexpr.ServiceExpr
		// Toolsets is the collection of toolset expressions exported
		// by the service.
		Toolsets []*ToolsetExpr
	}

	// AgentMethodExpr describes a routing strategy for an agent method.
	AgentMethodExpr struct {
		eval.DSLFunc

		// Name is the name of the method (must match a tool name in the agent's exported toolsets).
		Name string
		// Agent is the agent expression that owns this method.
		Agent *AgentExpr
		// Passthrough defines deterministic forwarding to another toolset/tool.
		Passthrough *PassthroughExpr
	}

	// PassthroughExpr defines deterministic forwarding to another toolset/tool.
	PassthroughExpr struct {
		// Toolset is the name of the target toolset.
		Toolset string
		// Tool is the name of the target tool.
		Tool string
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
	if len(a.Methods) > 0 {
		walk(eval.ToExpressionSet(a.Methods))
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

// EvalName is part of eval.Expression allowing descriptive error messages.
func (s *ServiceExportsExpr) EvalName() string {
	if s.Service == nil {
		return "service exports"
	}
	return fmt.Sprintf("service exports for %q", s.Service.Name)
}

// EvalName is part of eval.Expression allowing descriptive error messages.
func (m *AgentMethodExpr) EvalName() string {
	return fmt.Sprintf("method %q (agent %q)", m.Name, m.Agent.Name)
}
