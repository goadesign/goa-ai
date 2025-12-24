package ir

import (
	"fmt"
	"slices"
	"strings"

	"goa.design/goa-ai/codegen/naming"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type toolsetRefKind string

const (
	toolsetRefUsed        toolsetRefKind = "used"
	toolsetRefExported    toolsetRefKind = "exported"
	toolsetRefServiceExpo toolsetRefKind = "service_export"
)

type toolsetRef struct {
	kind toolsetRefKind

	serviceName     string
	servicePathName string

	agentName string
	agentSlug string
}

// Build constructs a Design IR from evaluated Goa roots.
func Build(genpkg string, roots []eval.Root) (*Design, error) {
	var agentsRoot *agentsExpr.RootExpr
	for _, root := range roots {
		if r, ok := root.(*agentsExpr.RootExpr); ok {
			agentsRoot = r
			break
		}
	}
	if agentsRoot == nil {
		return nil, fmt.Errorf("agent root not found in eval roots")
	}

	var goaRoot *goaexpr.RootExpr
	for _, root := range roots {
		if r, ok := root.(*goaexpr.RootExpr); ok {
			goaRoot = r
			break
		}
	}
	if goaRoot == nil {
		return nil, fmt.Errorf("goa root not found in eval roots")
	}

	servicesData := service.NewServicesData(goaRoot)
	servicesByName := make(map[string]*Service, len(goaRoot.Services))
	for _, svc := range goaRoot.Services {
		if svc == nil {
			continue
		}
		sd := servicesData.Get(svc.Name)
		if sd == nil {
			continue
		}
		servicesByName[sd.Name] = &Service{
			Name:     sd.Name,
			PathName: sd.PathName,
			Goa:      sd,
		}
	}

	agents := make([]*Agent, 0, len(agentsRoot.Agents))
	for _, ae := range agentsRoot.Agents {
		if ae == nil || ae.Service == nil {
			continue
		}
		svc := servicesByName[ae.Service.Name]
		if svc == nil {
			return nil, fmt.Errorf("service %q not found for agent %q", ae.Service.Name, ae.Name)
		}
		agents = append(agents, &Agent{
			Name:    ae.Name,
			Slug:    naming.SanitizeToken(ae.Name, "agent"),
			Service: svc,
		})
	}
	slices.SortFunc(agents, func(a, b *Agent) int {
		if d := strings.Compare(a.Service.Name, b.Service.Name); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})

	// Collect toolset refs grouped by defining/origin toolset name.
	refsByToolset := make(map[string][]toolsetRef)
	recordToolset := func(ts *agentsExpr.ToolsetExpr, kind toolsetRefKind, svc *Service, agent *Agent) {
		if ts == nil {
			return
		}
		def := ts
		if ts.Origin != nil {
			def = ts.Origin
		}
		if def == nil || def.Name == "" {
			return
		}
		ref := toolsetRef{
			kind:            kind,
			serviceName:     svc.Name,
			servicePathName: svc.PathName,
		}
		if agent != nil {
			ref.agentName = agent.Name
			ref.agentSlug = agent.Slug
		}
		refsByToolset[def.Name] = append(refsByToolset[def.Name], ref)
	}

	agentsByServiceAgent := make(map[string]*Agent, len(agents))
	for _, a := range agents {
		agentsByServiceAgent[a.Service.Name+"|"+a.Name] = a
	}

	for _, ae := range agentsRoot.Agents {
		if ae == nil || ae.Service == nil {
			continue
		}
		svc := servicesByName[ae.Service.Name]
		if svc == nil {
			return nil, fmt.Errorf("service %q not found for agent %q", ae.Service.Name, ae.Name)
		}
		ag := agentsByServiceAgent[svc.Name+"|"+ae.Name]
		if ag == nil {
			return nil, fmt.Errorf("agent %q not found for service %q", ae.Name, svc.Name)
		}
		if ae.Used != nil {
			for _, ts := range ae.Used.Toolsets {
				recordToolset(ts, toolsetRefUsed, svc, ag)
			}
		}
		if ae.Exported != nil {
			for _, ts := range ae.Exported.Toolsets {
				recordToolset(ts, toolsetRefExported, svc, ag)
			}
		}
	}
	for _, se := range agentsRoot.ServiceExports {
		if se == nil || se.Service == nil {
			continue
		}
		svc := servicesByName[se.Service.Name]
		if svc == nil {
			return nil, fmt.Errorf("service %q not found for service exports", se.Service.Name)
		}
		for _, ts := range se.Toolsets {
			recordToolset(ts, toolsetRefServiceExpo, svc, nil)
		}
	}

	// Enumerate defining toolsets (Origin == nil) from all reachable toolsets.
	defToolsets := make(map[string]*agentsExpr.ToolsetExpr)
	consider := func(ts *agentsExpr.ToolsetExpr) {
		if ts == nil || ts.Name == "" || ts.Origin != nil {
			return
		}
		defToolsets[ts.Name] = ts
	}
	for _, ts := range agentsRoot.Toolsets {
		consider(ts)
	}
	for _, se := range agentsRoot.ServiceExports {
		if se == nil {
			continue
		}
		for _, ts := range se.Toolsets {
			consider(ts)
		}
	}
	for _, ae := range agentsRoot.Agents {
		if ae == nil {
			continue
		}
		if ae.Exported != nil {
			for _, ts := range ae.Exported.Toolsets {
				consider(ts)
			}
		}
	}
	for _, ae := range agentsRoot.Agents {
		if ae == nil {
			continue
		}
		if ae.Used != nil {
			for _, ts := range ae.Used.Toolsets {
				consider(ts)
			}
		}
	}

	toolsets := make([]*Toolset, 0, len(defToolsets))
	for name, def := range defToolsets {
		owner, err := selectOwner(def, refsByToolset[name], servicesByName)
		if err != nil {
			return nil, err
		}
		toolsets = append(toolsets, &Toolset{
			Name:  name,
			Slug:  naming.SanitizeToken(name, "toolset"),
			Owner: owner,
		})
	}
	slices.SortFunc(toolsets, func(a, b *Toolset) int {
		return strings.Compare(a.Name, b.Name)
	})

	services := make([]*Service, 0, len(servicesByName))
	for _, s := range servicesByName {
		services = append(services, s)
	}
	slices.SortFunc(services, func(a, b *Service) int {
		return strings.Compare(a.Name, b.Name)
	})

	return &Design{
		Genpkg:   genpkg,
		Services: services,
		Agents:   agents,
		Toolsets: toolsets,
	}, nil
}

func selectOwner(def *agentsExpr.ToolsetExpr, refs []toolsetRef, servicesByName map[string]*Service) (Owner, error) {
	if def != nil && def.Provider != nil && def.Provider.Kind == agentsExpr.ProviderMCP && def.Provider.MCPService != "" {
		svc := servicesByName[def.Provider.MCPService]
		if svc == nil {
			return Owner{}, fmt.Errorf("toolset %q references unknown MCP service %q", def.Name, def.Provider.MCPService)
		}
		return Owner{
			Kind:            OwnerKindService,
			ServiceName:     svc.Name,
			ServicePathName: svc.PathName,
		}, nil
	}

	exported := make([]toolsetRef, 0, len(refs))
	for _, r := range refs {
		if r.kind == toolsetRefExported {
			exported = append(exported, r)
		}
	}
	if len(exported) > 0 {
		slices.SortFunc(exported, func(a, b toolsetRef) int {
			if d := strings.Compare(a.serviceName, b.serviceName); d != 0 {
				return d
			}
			return strings.Compare(a.agentName, b.agentName)
		})
		r := exported[0]
		return Owner{
			Kind:            OwnerKindAgentExport,
			ServiceName:     r.serviceName,
			ServicePathName: r.servicePathName,
			AgentName:       r.agentName,
			AgentSlug:       r.agentSlug,
		}, nil
	}

	serviceExports := make([]toolsetRef, 0, len(refs))
	for _, r := range refs {
		if r.kind == toolsetRefServiceExpo {
			serviceExports = append(serviceExports, r)
		}
	}
	if len(serviceExports) > 0 {
		slices.SortFunc(serviceExports, func(a, b toolsetRef) int {
			return strings.Compare(a.serviceName, b.serviceName)
		})
		r := serviceExports[0]
		return Owner{
			Kind:            OwnerKindService,
			ServiceName:     r.serviceName,
			ServicePathName: r.servicePathName,
		}, nil
	}

	if len(refs) == 0 {
		return Owner{}, fmt.Errorf("toolset %q has no owning references", def.Name)
	}
	slices.SortFunc(refs, func(a, b toolsetRef) int {
		return strings.Compare(a.serviceName, b.serviceName)
	})
	r := refs[0]
	return Owner{
		Kind:            OwnerKindService,
		ServiceName:     r.serviceName,
		ServicePathName: r.servicePathName,
	}, nil
}
