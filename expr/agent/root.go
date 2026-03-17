package agent

import (
	"fmt"
	"sort"

	"goa.design/goa-ai/codegen/naming"
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

type (
	toolsetOwnerRefKind string

	toolsetOwnerRef struct {
		kind        toolsetOwnerRefKind
		serviceName string
		agentName   string
		agentSlug   string
	}
)

const (
	toolsetOwnerRefUsed          toolsetOwnerRefKind = "used"
	toolsetOwnerRefExported      toolsetOwnerRefKind = "exported"
	toolsetOwnerRefServiceExport toolsetOwnerRefKind = "service_export"
)

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

	total := 0
	for _, ts := range toolsets {
		total += len(ts.Tools)
	}
	tools := make([]*ToolExpr, 0, total)
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
	r.validateSanitizedAgentSlugs(verr)

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
	sanitizedToolsets := make(map[string]*ToolsetExpr)
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
			if other == ts {
				return
			}
			verr.Add(ts, "toolset name %q duplicates a toolset declared in %s", ts.Name, other.EvalName())
			return
		}
		toolsets[ts.Name] = ts
	}
	record := func(ts *ToolsetExpr, scopeKey, scopeLabel string) {
		r.recordSanitizedToolsetSlug(verr, sanitizedToolsets, ts, scopeKey, scopeLabel)
		// Only defining toolsets need globally unique identities and per-toolset
		// duplicate-tool checks. Referenced toolsets still participate in
		// sanitized-slug validation because consumer-side generated paths and local
		// helper names derive from the referenced alias.
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
		record(ts, "top-level", "top-level toolsets")
	}
	// Agent Used/Exported toolsets.
	for _, a := range r.Agents {
		if a.Used != nil {
			for _, ts := range a.Used.Toolsets {
				record(ts, r.agentToolsetScopeKey(a), r.agentToolsetScopeLabel(a))
			}
		}
		if a.Exported != nil {
			for _, ts := range a.Exported.Toolsets {
				record(ts, r.agentToolsetScopeKey(a), r.agentToolsetScopeLabel(a))
			}
		}
	}
	for _, se := range r.ServiceExports {
		for _, ts := range se.Toolsets {
			record(ts, r.serviceExportScopeKey(se), r.serviceExportScopeLabel(se))
		}
	}
	r.validateOwnerScopedToolsetSlugs(verr)

	return verr
}

func (r *RootExpr) validateSanitizedAgentSlugs(verr *eval.ValidationErrors) {
	agents := make(map[string]*AgentExpr)
	for _, agent := range r.Agents {
		slug := naming.SanitizeToken(agent.Name, "agent")
		key := agent.Service.Name + ":" + slug
		if other, dup := agents[key]; dup {
			verr.Add(
				agent,
				"sanitized agent name %q duplicates an agent declared in %s within service %q",
				slug,
				other.EvalName(),
				agent.Service.Name,
			)
			continue
		}
		agents[key] = agent
	}
}

func (r *RootExpr) recordSanitizedToolsetSlug(
	verr *eval.ValidationErrors,
	toolsets map[string]*ToolsetExpr,
	ts *ToolsetExpr,
	scopeKey string,
	scopeLabel string,
) {
	if ts.Name == "" {
		return
	}
	slug := naming.SanitizeToken(ts.Name, "toolset")
	key := scopeKey + ":" + slug
	if other, dup := toolsets[key]; dup {
		if sameToolsetOrigin(other, ts) {
			return
		}
		verr.Add(
			ts,
			"sanitized toolset name %q duplicates a toolset declared in %s within %s",
			slug,
			other.EvalName(),
			scopeLabel,
		)
		return
	}
	toolsets[key] = ts
}

func sameToolsetOrigin(left, right *ToolsetExpr) bool {
	if left == nil || right == nil {
		return false
	}
	return canonicalToolset(left) == canonicalToolset(right)
}

func canonicalToolset(ts *ToolsetExpr) *ToolsetExpr {
	if ts == nil {
		return nil
	}
	if ts.Origin != nil {
		return ts.Origin
	}
	return ts
}

func (r *RootExpr) agentToolsetScopeKey(agent *AgentExpr) string {
	return agent.Service.Name + ":" + naming.SanitizeToken(agent.Name, "agent")
}

func (r *RootExpr) agentToolsetScopeLabel(agent *AgentExpr) string {
	return fmt.Sprintf("agent %q in service %q", agent.Name, agent.Service.Name)
}

func (r *RootExpr) serviceExportScopeKey(se *ServiceExportsExpr) string {
	return "service:" + se.Service.Name
}

func (r *RootExpr) serviceExportScopeLabel(se *ServiceExportsExpr) string {
	return fmt.Sprintf("service exports for %q", se.Service.Name)
}

// validateOwnerScopedToolsetSlugs mirrors the ownership precedence used by code
// generation so defining toolsets that land in the same owner-scoped output
// package are rejected during DSL validation.
func (r *RootExpr) validateOwnerScopedToolsetSlugs(verr *eval.ValidationErrors) {
	owners := make(map[string]*ToolsetExpr)
	refs := r.collectToolsetOwnerRefs()
	for _, ts := range r.definingToolsetsForOwnerValidation() {
		namespace, ok := r.toolsetOwnerNamespace(ts, refs[ts])
		if !ok {
			continue
		}
		slug := naming.SanitizeToken(ts.Name, "toolset")
		key := namespace + ":" + slug
		if other, dup := owners[key]; dup {
			if other == ts {
				continue
			}
			verr.Add(
				ts,
				"sanitized toolset name %q duplicates a toolset declared in %s once owner-scoped generation is applied",
				slug,
				other.EvalName(),
			)
			continue
		}
		owners[key] = ts
	}
}

// collectToolsetOwnerRefs records every Use/Export reference keyed by the
// defining toolset so owner-scoped validation can replay generator precedence
// without importing codegen packages into the expr layer.
func (r *RootExpr) collectToolsetOwnerRefs() map[*ToolsetExpr][]toolsetOwnerRef {
	refs := make(map[*ToolsetExpr][]toolsetOwnerRef)
	record := func(ts *ToolsetExpr, kind toolsetOwnerRefKind, serviceName, agentName string) {
		def := canonicalToolset(ts)
		if def == nil || def.Name == "" {
			return
		}
		ref := toolsetOwnerRef{
			kind:        kind,
			serviceName: serviceName,
			agentName:   agentName,
			agentSlug:   naming.SanitizeToken(agentName, "agent"),
		}
		refs[def] = append(refs[def], ref)
	}
	for _, agent := range r.Agents {
		if agent == nil || agent.Service == nil {
			continue
		}
		if agent.Used != nil {
			for _, ts := range agent.Used.Toolsets {
				record(ts, toolsetOwnerRefUsed, agent.Service.Name, agent.Name)
			}
		}
		if agent.Exported != nil {
			for _, ts := range agent.Exported.Toolsets {
				record(ts, toolsetOwnerRefExported, agent.Service.Name, agent.Name)
			}
		}
	}
	for _, se := range r.ServiceExports {
		if se == nil || se.Service == nil {
			continue
		}
		for _, ts := range se.Toolsets {
			record(ts, toolsetOwnerRefServiceExport, se.Service.Name, "")
		}
	}
	return refs
}

// definingToolsetsForOwnerValidation returns each defining toolset exactly once
// regardless of whether it was declared top-level, inline under Use/Export, or
// inside a service export block.
func (r *RootExpr) definingToolsetsForOwnerValidation() []*ToolsetExpr {
	seen := make(map[*ToolsetExpr]struct{})
	var toolsets []*ToolsetExpr
	record := func(ts *ToolsetExpr) {
		if ts == nil || ts.Name == "" || ts.Origin != nil {
			return
		}
		if _, ok := seen[ts]; ok {
			return
		}
		seen[ts] = struct{}{}
		toolsets = append(toolsets, ts)
	}
	for _, ts := range r.Toolsets {
		record(ts)
	}
	for _, agent := range r.Agents {
		if agent == nil {
			continue
		}
		if agent.Used != nil {
			for _, ts := range agent.Used.Toolsets {
				record(ts)
			}
		}
		if agent.Exported != nil {
			for _, ts := range agent.Exported.Toolsets {
				record(ts)
			}
		}
	}
	for _, se := range r.ServiceExports {
		if se == nil {
			continue
		}
		for _, ts := range se.Toolsets {
			record(ts)
		}
	}
	return toolsets
}

// toolsetOwnerNamespace mirrors the generator's ownership precedence:
// provider-owned MCP toolsets first, then agent exports, then service exports,
// then the first consumer service.
func (r *RootExpr) toolsetOwnerNamespace(ts *ToolsetExpr, refs []toolsetOwnerRef) (string, bool) {
	if ts.Provider != nil && ts.Provider.Kind == ProviderMCP && ts.Provider.MCPService != "" {
		return "service:" + ts.Provider.MCPService, true
	}
	exported := filterToolsetOwnerRefs(refs, toolsetOwnerRefExported)
	if len(exported) > 0 {
		sort.Slice(exported, func(i, j int) bool {
			if exported[i].serviceName != exported[j].serviceName {
				return exported[i].serviceName < exported[j].serviceName
			}
			return exported[i].agentName < exported[j].agentName
		})
		ref := exported[0]
		return "agent-export:" + ref.serviceName + ":" + ref.agentSlug, true
	}
	serviceExports := filterToolsetOwnerRefs(refs, toolsetOwnerRefServiceExport)
	if len(serviceExports) > 0 {
		sort.Slice(serviceExports, func(i, j int) bool {
			return serviceExports[i].serviceName < serviceExports[j].serviceName
		})
		return "service:" + serviceExports[0].serviceName, true
	}
	if len(refs) == 0 {
		return "", false
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].serviceName < refs[j].serviceName
	})
	return "service:" + refs[0].serviceName, true
}

// filterToolsetOwnerRefs extracts one ref class while preserving the collected
// values for later deterministic sorting.
func filterToolsetOwnerRefs(refs []toolsetOwnerRef, kind toolsetOwnerRefKind) []toolsetOwnerRef {
	selected := make([]toolsetOwnerRef, 0, len(refs))
	for _, ref := range refs {
		if ref.kind == kind {
			selected = append(selected, ref)
		}
	}
	return selected
}
