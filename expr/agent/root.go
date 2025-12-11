package agent

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// RootExpr represents the top-level root for all agent and toolset
// declarations.
type RootExpr struct {
	// Agents is the collection of all agent expressions defined in the
	// design.
	Agents []*AgentExpr
	// ServiceExports holds toolsets exported directly by services.
	ServiceExports []*ServiceExportsExpr
	// Toolsets is the collection of all standalone toolset expressions not
	// owned by an agent.
	Toolsets []*ToolsetExpr
	// Registries is the collection of all registry expressions defined
	// in the design.
	Registries []*RegistryExpr
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
	// Walk registries first since toolsets may reference them.
	if len(r.Registries) > 0 {
		walk(eval.ToExpressionSet(r.Registries))
	}

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
	for _, se := range r.ServiceExports {
		if se != nil {
			groups = append(groups, se)
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
	for _, se := range r.ServiceExports {
		if se != nil {
			toolsets = append(toolsets, se.Toolsets...)
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
// agent, toolset, and registry declarations. In particular:
//   - Registry names must be globally unique.
//   - Defining toolsets (Origin == nil) must use globally unique names so
//     they can serve as stable identifiers.
//   - Tool names must be unique within a defining toolset (Origin == nil)
//     but may be reused across different toolsets. Qualified tool IDs are
//     derived as "toolset.tool".
func (r *RootExpr) Validate() error {
	verr := new(eval.ValidationErrors)

	// Validate registry name uniqueness.
	registries := make(map[string]*RegistryExpr)
	for _, reg := range r.Registries {
		if other, dup := registries[reg.Name]; dup {
			verr.Add(reg, "registry name %q duplicates a registry declared in %s", reg.Name, other.EvalName())
			continue
		}
		registries[reg.Name] = reg
	}

	toolsets := make(map[string]*ToolsetExpr)
	recordToolset := func(ts *ToolsetExpr) {
		// Only enforce uniqueness on defining/origin toolsets; references
		// inherit the origin name.
		if ts.Origin != nil {
			return
		}
		if ts.Name == "" {
			return
		}
		if other, dup := toolsets[ts.Name]; dup {
			verr.Add(ts, "toolset name %q duplicates a toolset declared in %s", ts.Name, other.EvalName())
			return
		}
		toolsets[ts.Name] = ts
	}
	record := func(ts *ToolsetExpr) {
		// Only enforce uniqueness on defining/origin toolsets to avoid counting
		// the same tool multiple times when a toolset is referenced via Uses/Toolset().
		if ts.Origin != nil {
			return
		}
		// Record defining toolset names first to enforce global uniqueness.
		recordToolset(ts)
		// Enforce per-toolset uniqueness for tool names while allowing the
		// same tool name to appear in multiple toolsets.
		local := make(map[string]*ToolExpr)
		for _, t := range ts.Tools {
			name := t.Name
			if name == "" {
				continue
			}
			if other, dup := local[name]; dup {
				verr.Add(t, "tool name %q duplicates a tool declared in %s", name, other.EvalName())
				continue
			}
			local[name] = t
		}
	}
	// Top-level toolsets.
	for _, ts := range r.Toolsets {
		record(ts)
	}
	// Agent Used/Exported toolsets.
	for _, a := range r.Agents {
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

	exported := make(map[*ToolsetExpr]struct{})

	// Agent-level exports.
	for _, a := range r.Agents {
		if a.Exported == nil {
			continue
		}
		for _, ts := range a.Exported.Toolsets {
			exported[ts] = struct{}{}
		}
	}

	// Service-level exports.
	for _, se := range r.ServiceExports {
		for _, ts := range se.Toolsets {
			exported[ts] = struct{}{}
		}
	}

	return verr
}
