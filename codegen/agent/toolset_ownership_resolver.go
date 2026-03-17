// Package codegen resolves toolset ownership directly from evaluated Goa roots.
//
// This file is the single source of truth for where shared toolset-owned
// artifacts live. The resolver deliberately works from evaluated roots rather
// than a separate IR so the generator sees the same ownership facts everywhere:
// aliases collapse to their defining Origin, MCP-backed toolsets are owned by
// the provider service, and non-MCP toolsets select one deterministic owner
// using export-first precedence. If ownership cannot be resolved, generation
// fails fast instead of emitting duplicate or conflicting packages.
package codegen

import (
	"fmt"
	"slices"
	"strings"

	"goa.design/goa-ai/codegen/naming"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// toolsetOwnerKind classifies the canonical output anchor selected for a
	// defining toolset.
	toolsetOwnerKind string

	// resolvedToolsetOwner records the canonical location that owns one defining
	// toolset. Downstream generators use this to derive specs/codec packages and,
	// for exported toolsets, provider-side agenttools helpers.
	resolvedToolsetOwner struct {
		kind toolsetOwnerKind

		serviceName     string
		servicePathName string

		agentName string
		agentSlug string
	}

	// toolsetOwnershipRefKind identifies how a toolset is referenced by the
	// evaluated design. The ref kind drives owner precedence.
	toolsetOwnershipRefKind string

	// toolsetOwnershipRef captures one concrete reference to a defining toolset
	// from an agent Use/Export or a service-level export. References are reduced
	// to names and slugs so selection stays deterministic and independent from the
	// original expression graph after collection.
	toolsetOwnershipRef struct {
		kind toolsetOwnershipRefKind

		serviceName     string
		servicePathName string

		agentName string
		agentSlug string
	}

	// toolsetOwnershipService is the minimal service identity needed by the
	// ownership resolver: the Goa name and the stable generated path segment.
	toolsetOwnershipService struct {
		name     string
		pathName string
	}

	// toolsetOwnershipAgent is the minimal agent identity needed when an exported
	// toolset is owned by an agent-scoped package.
	toolsetOwnershipAgent struct {
		name    string
		slug    string
		service *toolsetOwnershipService
	}
)

const (
	// toolsetOwnerKindService places shared artifacts under the owning service.
	toolsetOwnerKindService toolsetOwnerKind = "service"
	// toolsetOwnerKindAgentExport places shared artifacts under an agent export.
	toolsetOwnerKindAgentExport toolsetOwnerKind = "agent_export"

	// toolsetOwnershipRefUsed records a Use reference from an agent.
	toolsetOwnershipRefUsed toolsetOwnershipRefKind = "used"
	// toolsetOwnershipRefExported records an agent Export reference.
	toolsetOwnershipRefExported toolsetOwnershipRefKind = "exported"
	// toolsetOwnershipRefServiceExport records a service-level Export reference.
	toolsetOwnershipRefServiceExport toolsetOwnershipRefKind = "service_export"
)

// buildToolsetOwners resolves the canonical owner for every defining toolset in
// the evaluated design roots. The returned map is keyed by defining toolset
// name so aliases and Use/Export references share one ownership decision.
func buildToolsetOwners(roots []eval.Root) (map[string]resolvedToolsetOwner, error) {
	agentsRoot, err := findAgentsRoot(roots)
	if err != nil {
		return nil, err
	}
	goaRoot, err := findGoaRoot(roots)
	if err != nil {
		return nil, err
	}

	servicesByName := collectOwnershipServices(goaRoot)
	agentsByKey, err := collectOwnershipAgents(agentsRoot, servicesByName)
	if err != nil {
		return nil, err
	}
	refsByToolset, err := collectToolsetOwnershipRefs(agentsRoot, servicesByName, agentsByKey)
	if err != nil {
		return nil, err
	}

	owners := make(map[string]resolvedToolsetOwner)
	paths := make(map[string]string)
	for name, def := range collectDefiningToolsets(agentsRoot) {
		owner, err := selectToolsetOwner(def, refsByToolset[name], servicesByName)
		if err != nil {
			return nil, err
		}
		key := resolvedToolsetOwnerCollisionKey(owner, name)
		if other, ok := paths[key]; ok {
			return nil, fmt.Errorf(
				`toolset %q collides with toolset %q on owner-scoped generated path %q`,
				name,
				other,
				key,
			)
		}
		paths[key] = name
		owners[name] = owner
	}
	return owners, nil
}

// findAgentsRoot returns the evaluated agent root from the Goa eval roots.
func findAgentsRoot(roots []eval.Root) (*agentsExpr.RootExpr, error) {
	for _, root := range roots {
		agentsRoot, ok := root.(*agentsExpr.RootExpr)
		if ok {
			return agentsRoot, nil
		}
	}
	return nil, fmt.Errorf("agent root not found in eval roots")
}

// findGoaRoot returns the evaluated Goa root from the Goa eval roots.
func findGoaRoot(roots []eval.Root) (*goaexpr.RootExpr, error) {
	for _, root := range roots {
		goaRoot, ok := root.(*goaexpr.RootExpr)
		if ok {
			return goaRoot, nil
		}
	}
	return nil, fmt.Errorf("goa root not found in eval roots")
}

// collectOwnershipServices maps Goa services to the stable path names used by
// generated packages. The returned map is keyed by Goa service name because all
// later ownership checks start from DSL references.
func collectOwnershipServices(root *goaexpr.RootExpr) map[string]*toolsetOwnershipService {
	servicesByName := make(map[string]*toolsetOwnershipService, len(root.Services))
	for _, svc := range root.Services {
		if svc == nil {
			continue
		}
		servicesByName[svc.Name] = &toolsetOwnershipService{
			name:     svc.Name,
			pathName: ownershipServicePathName(svc.Name),
		}
	}
	return servicesByName
}

// collectOwnershipAgents builds a lookup for agent-owned references so Export
// ownership can record both the agent name and the sanitized slug used by
// generated directories.
func collectOwnershipAgents(root *agentsExpr.RootExpr, servicesByName map[string]*toolsetOwnershipService) (map[string]*toolsetOwnershipAgent, error) {
	agentsByKey := make(map[string]*toolsetOwnershipAgent, len(root.Agents))
	for _, agent := range root.Agents {
		if agent == nil || agent.Service == nil {
			continue
		}
		svc := servicesByName[agent.Service.Name]
		if svc == nil {
			return nil, fmt.Errorf("service %q not found for agent %q", agent.Service.Name, agent.Name)
		}
		key := ownershipAgentKey(svc.name, agent.Name)
		agentsByKey[key] = &toolsetOwnershipAgent{
			name:    agent.Name,
			slug:    naming.SanitizeToken(agent.Name, "agent"),
			service: svc,
		}
	}
	return agentsByKey, nil
}

// collectToolsetOwnershipRefs groups all Used/Export/Service Export references
// by defining toolset name so owner selection sees the full design context
// before applying precedence. Every alias is normalized to its Origin by
// recordToolsetOwnershipRef.
func collectToolsetOwnershipRefs(root *agentsExpr.RootExpr, servicesByName map[string]*toolsetOwnershipService, agentsByKey map[string]*toolsetOwnershipAgent) (map[string][]toolsetOwnershipRef, error) {
	refsByToolset := make(map[string][]toolsetOwnershipRef)
	record := func(ts *agentsExpr.ToolsetExpr, kind toolsetOwnershipRefKind, svc *toolsetOwnershipService, agent *toolsetOwnershipAgent) {
		recordToolsetOwnershipRef(refsByToolset, ts, kind, svc, agent)
	}

	for _, agentExpr := range root.Agents {
		if agentExpr == nil || agentExpr.Service == nil {
			continue
		}
		svc := servicesByName[agentExpr.Service.Name]
		if svc == nil {
			return nil, fmt.Errorf("service %q not found for agent %q", agentExpr.Service.Name, agentExpr.Name)
		}
		agent := agentsByKey[ownershipAgentKey(svc.name, agentExpr.Name)]
		if agent == nil {
			return nil, fmt.Errorf("agent %q not found for service %q", agentExpr.Name, svc.name)
		}
		if agentExpr.Used != nil {
			for _, ts := range agentExpr.Used.Toolsets {
				record(ts, toolsetOwnershipRefUsed, svc, agent)
			}
		}
		if agentExpr.Exported != nil {
			for _, ts := range agentExpr.Exported.Toolsets {
				record(ts, toolsetOwnershipRefExported, svc, agent)
			}
		}
	}

	for _, serviceExport := range root.ServiceExports {
		if serviceExport == nil || serviceExport.Service == nil {
			continue
		}
		svc := servicesByName[serviceExport.Service.Name]
		if svc == nil {
			return nil, fmt.Errorf("service %q not found for service exports", serviceExport.Service.Name)
		}
		for _, ts := range serviceExport.Toolsets {
			record(ts, toolsetOwnershipRefServiceExport, svc, nil)
		}
	}
	return refsByToolset, nil
}

// recordToolsetOwnershipRef appends one ownership reference keyed by the
// defining toolset name. Alias toolsets always collapse to their Origin so the
// rest of the resolver never has to reason about both aliases and definitions.
func recordToolsetOwnershipRef(refsByToolset map[string][]toolsetOwnershipRef, ts *agentsExpr.ToolsetExpr, kind toolsetOwnershipRefKind, svc *toolsetOwnershipService, agent *toolsetOwnershipAgent) {
	if ts == nil || svc == nil {
		return
	}
	def := ts
	if ts.Origin != nil {
		def = ts.Origin
	}
	if def == nil || def.Name == "" {
		return
	}

	ref := toolsetOwnershipRef{
		kind:            kind,
		serviceName:     svc.name,
		servicePathName: svc.pathName,
	}
	if agent != nil {
		ref.agentName = agent.name
		ref.agentSlug = agent.slug
	}
	refsByToolset[def.Name] = append(refsByToolset[def.Name], ref)
}

// collectDefiningToolsets returns every defining toolset reachable from the
// agent root. Referenced aliases are ignored because their ownership is derived
// from the Origin toolset, and duplicate names naturally collapse to one map
// entry because root validation already enforces uniqueness.
func collectDefiningToolsets(root *agentsExpr.RootExpr) map[string]*agentsExpr.ToolsetExpr {
	toolsets := make(map[string]*agentsExpr.ToolsetExpr)
	consider := func(ts *agentsExpr.ToolsetExpr) {
		if ts == nil || ts.Name == "" || ts.Origin != nil {
			return
		}
		toolsets[ts.Name] = ts
	}

	for _, ts := range root.Toolsets {
		consider(ts)
	}
	for _, serviceExport := range root.ServiceExports {
		if serviceExport == nil {
			continue
		}
		for _, ts := range serviceExport.Toolsets {
			consider(ts)
		}
	}
	for _, agent := range root.Agents {
		if agent == nil {
			continue
		}
		if agent.Exported != nil {
			for _, ts := range agent.Exported.Toolsets {
				consider(ts)
			}
		}
		if agent.Used != nil {
			for _, ts := range agent.Used.Toolsets {
				consider(ts)
			}
		}
	}
	return toolsets
}

// selectToolsetOwner chooses the canonical generation owner for one defining
// toolset. MCP-backed toolsets are always owned by the provider service. Other
// toolsets prefer agent exports, then service exports, then the first consumer.
// The sort order inside each branch makes the result deterministic when several
// valid references exist.
func selectToolsetOwner(def *agentsExpr.ToolsetExpr, refs []toolsetOwnershipRef, servicesByName map[string]*toolsetOwnershipService) (resolvedToolsetOwner, error) {
	if def != nil && def.Provider != nil && def.Provider.Kind == agentsExpr.ProviderMCP && def.Provider.MCPService != "" {
		svc := servicesByName[def.Provider.MCPService]
		if svc == nil {
			return resolvedToolsetOwner{}, fmt.Errorf("toolset %q references unknown MCP service %q", def.Name, def.Provider.MCPService)
		}
		return resolvedToolsetOwner{
			kind:            toolsetOwnerKindService,
			serviceName:     svc.name,
			servicePathName: svc.pathName,
		}, nil
	}

	exported := selectToolsetRefs(refs, toolsetOwnershipRefExported)
	if len(exported) > 0 {
		slices.SortFunc(exported, func(left, right toolsetOwnershipRef) int {
			if delta := strings.Compare(left.serviceName, right.serviceName); delta != 0 {
				return delta
			}
			return strings.Compare(left.agentName, right.agentName)
		})
		ref := exported[0]
		return resolvedToolsetOwner{
			kind:            toolsetOwnerKindAgentExport,
			serviceName:     ref.serviceName,
			servicePathName: ref.servicePathName,
			agentName:       ref.agentName,
			agentSlug:       ref.agentSlug,
		}, nil
	}

	serviceExports := selectToolsetRefs(refs, toolsetOwnershipRefServiceExport)
	if len(serviceExports) > 0 {
		slices.SortFunc(serviceExports, func(left, right toolsetOwnershipRef) int {
			return strings.Compare(left.serviceName, right.serviceName)
		})
		ref := serviceExports[0]
		return resolvedToolsetOwner{
			kind:            toolsetOwnerKindService,
			serviceName:     ref.serviceName,
			servicePathName: ref.servicePathName,
		}, nil
	}

	if len(refs) == 0 {
		return resolvedToolsetOwner{}, fmt.Errorf("toolset %q has no owning references", def.Name)
	}
	slices.SortFunc(refs, func(left, right toolsetOwnershipRef) int {
		return strings.Compare(left.serviceName, right.serviceName)
	})
	ref := refs[0]
	return resolvedToolsetOwner{
		kind:            toolsetOwnerKindService,
		serviceName:     ref.serviceName,
		servicePathName: ref.servicePathName,
	}, nil
}

// selectToolsetRefs filters refs by kind into a fresh slice so callers can sort
// the result without mutating the full reference set.
func selectToolsetRefs(refs []toolsetOwnershipRef, kind toolsetOwnershipRefKind) []toolsetOwnershipRef {
	selected := make([]toolsetOwnershipRef, 0, len(refs))
	for _, ref := range refs {
		if ref.kind == kind {
			selected = append(selected, ref)
		}
	}
	return selected
}

// ownershipAgentKey builds the stable lookup key for one agent within one
// service. Agent names are only unique within their service, not globally.
func ownershipAgentKey(serviceName, agentName string) string {
	return serviceName + "|" + agentName
}

// ownershipServicePathName derives the generated service path segment using the
// same Goify+SnakeCase convention as Goa service data without forcing full
// service analysis during ownership resolution.
func ownershipServicePathName(serviceName string) string {
	return goacodegen.SnakeCase(goacodegen.Goify(serviceName, false))
}

// resolvedToolsetOwnerCollisionKey mirrors the exact namespace segments used by
// generated specs and agenttools packages so resolver-time collision checks fail
// on the same paths generation would otherwise overwrite.
func resolvedToolsetOwnerCollisionKey(owner resolvedToolsetOwner, toolsetName string) string {
	slug := naming.SanitizeToken(toolsetName, "toolset")
	switch owner.kind {
	case toolsetOwnerKindService:
		return strings.Join([]string{string(owner.kind), owner.servicePathName, slug}, "|")
	case toolsetOwnerKindAgentExport:
		return strings.Join([]string{string(owner.kind), owner.servicePathName, owner.agentSlug, slug}, "|")
	default:
		return strings.Join([]string{string(owner.kind), slug}, "|")
	}
}
