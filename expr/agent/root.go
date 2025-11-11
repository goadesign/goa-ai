package agent

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
	"strings"
)

// RootExpr represents the top-level root for all agent and toolset
// declarations.
type RootExpr struct {
	// Agents is the collection of all agent expressions defined in the
	// design.
	Agents []*AgentExpr
	// Toolsets is the collection of all standalone toolset expressions not
	// owned by an agent.
	Toolsets []*ToolsetExpr
	// DisableAgentDocs controls whether agent-specific documentation
	// generation is suppressed.
	DisableAgentDocs bool
}

// Root holds all agent DSL declarations for the current Goa design run.
var Root *RootExpr

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
	return []string{"goa.design/goa-ai/dsl"}
}

// WalkSets exposes the nested expressions to the eval engine.
func (r *RootExpr) WalkSets(walk eval.SetWalker) {
	walk(eval.ToExpressionSet(r.Agents))

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

	var tools []*ToolExpr
	for _, ts := range toolsets {
		tools = append(tools, ts.Tools...)
	}
	if len(tools) > 0 {
		walk(eval.ToExpressionSet(tools))
	}
}

// Validate enforces repository-wide invariants that require a view of all
// agent and toolset declarations. In particular, tool names must be globally
// unique so they can serve as simple, stable identifiers without qualification.
func (r *RootExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	seen := make(map[string]*ToolExpr)
	record := func(ts *ToolsetExpr) {
		if ts == nil {
			return
		}
		// Only enforce uniqueness on defining/origin toolsets to avoid counting
		// the same tool multiple times when a toolset is referenced via Uses/Toolset().
		if ts.Origin != nil {
			return
		}
		for _, t := range ts.Tools {
			if t == nil {
				continue
			}
			name := strings.TrimSpace(t.Name)
			if name == "" {
				continue
			}
			if other, dup := seen[name]; dup {
				verr.Add(t, "tool name %q duplicates a tool declared in %s", name, other.EvalName())
				continue
			}
			seen[name] = t
		}
	}
	// Top-level toolsets.
	for _, ts := range r.Toolsets {
		record(ts)
	}
	// Agent Used/Exported toolsets.
	for _, a := range r.Agents {
		if a == nil {
			continue
		}
		if a.Used != nil {
			for _, ts := range a.Used.Toolsets {
				record(ts)
			}
		}
		if a.Exported != nil {
			for _, ts := range a.Exported.Toolsets {
				record(ts)
			}
		}
	}
	return verr
}
