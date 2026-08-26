// Package codegen saves each generated registry client package and its Go names
// before any files are written.
package codegen

import (
	"fmt"
	"path"
	"strings"

	agentir "goa.design/goa-ai/codegen/ir"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// registryClientPlan stores the registry clients written under each service.
	registryClientPlan struct {
		byService  map[string][]*plannedRegistryClient
		services   []*agentir.Service
		clientData []*RegistryClientData
	}

	// plannedRegistryClient stores one registry definition and its generated
	// package names.
	plannedRegistryClient struct {
		registry *agentir.Registry
		pkg      *goacodegen.GeneratedPackage
		security map[*goaexpr.SchemeExpr]*plannedRegistrySecurity
	}

	// plannedRegistrySecurity stores the type and option function written for
	// one security scheme.
	plannedRegistrySecurity struct {
		authType *goacodegen.NameDeclaration
		option   *goacodegen.NameDeclaration
	}

	// registrySecurityNameOrder keeps security names in the same order even when
	// services or schemes are visited in a different order.
	registrySecurityNameOrder struct {
		service  string
		registry string
		scheme   string
		use      string
	}
)

// ComparePackageName orders two registry security names by service, registry,
// scheme, and use.
func (o registrySecurityNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(registrySecurityNameOrder)
	for _, values := range [][2]string{
		{o.service, right.service},
		{o.registry, right.registry},
		{o.scheme, right.scheme},
		{o.use, right.use},
	} {
		if compared := strings.Compare(values[0], values[1]); compared != 0 {
			return compared
		}
	}
	return 0
}

// planRegistryClients adds every registry package and declares all names
// written by its client and options files.
func planRegistryClients(generation *goacodegen.Generation, design *agentir.Design) (*registryClientPlan, error) {
	planned := &registryClientPlan{byService: make(map[string][]*plannedRegistryClient)}
	if len(design.Registries) == 0 {
		return planned, nil
	}
	for _, service := range design.Services {
		registries := registriesForService(design.Registries, service)
		if len(registries) == 0 {
			continue
		}
		planned.services = append(planned.services, service)
		for _, registry := range registries {
			client, err := planRegistryClient(generation, design.Genpkg, service, registry)
			if err != nil {
				return nil, err
			}
			planned.byService[service.Name] = append(planned.byService[service.Name], client)
		}
	}
	return planned, nil
}

// link adds the saved registry clients to the values used to write each
// service's files.
func (p *registryClientPlan) link(data *GeneratorData) {
	services := make(map[string]*ServiceAgentsData, len(data.Services))
	for _, service := range data.Services {
		services[service.Service.Name] = service
	}
	for _, service := range p.services {
		for _, planned := range p.byService[service.Name] {
			client := newRegistryClientData(
				data.Genpkg,
				service.PathName,
				planned.registry.Expr,
				planned,
			)
			p.clientData = append(p.clientData, client)
			if serviceData := services[service.Name]; serviceData != nil {
				serviceData.RegistryClients = append(serviceData.RegistryClients, client)
			}
		}
	}
}

// registriesForService returns the registry clients generated for one service.
// Services with agents or completions keep the existing complete registry set.
// Export-only services receive only registries named by their exports.
func registriesForService(registries []*agentir.Registry, service *agentir.Service) []*agentir.Registry {
	if len(service.Agents) > 0 || len(service.Completions) > 0 {
		return registries
	}
	names := make(map[string]struct{})
	for _, export := range service.Exports {
		if export.Provider == nil || export.Provider.Registry == nil {
			continue
		}
		names[export.Provider.Registry.RegistryName] = struct{}{}
	}
	selected := make([]*agentir.Registry, 0, len(names))
	for _, registry := range registries {
		if _, ok := names[registry.Name]; ok {
			selected = append(selected, registry)
		}
	}
	return selected
}

// planRegistryClient declares the package names written for one service and
// registry pair.
func planRegistryClient(
	generation *goacodegen.Generation,
	genpkg string,
	service *agentir.Service,
	registry *agentir.Registry,
) (*plannedRegistryClient, error) {
	importPath := path.Join(genpkg, service.PathName, "registry", goacodegen.SnakeCase(registry.Name))
	pkg, err := generation.ClaimPackage(importPath)
	if err != nil {
		return nil, fmt.Errorf("plan registry %q for service %q: %w", registry.Name, service.Name, err)
	}
	for _, spec := range registryClientPackageImports() {
		if err := pkg.RequireImport(spec); err != nil {
			return nil, fmt.Errorf("plan registry %q import %q: %w", registry.Name, spec.Path, err)
		}
	}
	fixed := []struct {
		kind goacodegen.PackageNameKind
		name string
	}{
		{goacodegen.NameType, "ToolsetInfo"},
		{goacodegen.NameType, "ToolsetSchema"},
		{goacodegen.NameType, "ToolSchema"},
		{goacodegen.NameType, "SearchResult"},
		{goacodegen.NameType, "AuthProvider"},
		{goacodegen.NameType, "Client"},
		{goacodegen.NameType, "SemanticSearchOptions"},
		{goacodegen.NameType, "SearchCapabilities"},
		{goacodegen.NameType, "RegistryError"},
		{goacodegen.NameType, "bytesReader"},
		{goacodegen.NameType, "Option"},
		{goacodegen.NameConstant, "pathToolsets"},
		{goacodegen.NameConstant, "pathSearch"},
		{goacodegen.NameConstant, "pathSemanticSearch"},
		{goacodegen.NameConstant, "pathCapabilities"},
		{goacodegen.NameFunction, "NewClient"},
		{goacodegen.NameFunction, "WithHTTPClient"},
		{goacodegen.NameFunction, "WithAuth"},
		{goacodegen.NameFunction, "WithTimeout"},
		{goacodegen.NameFunction, "WithRetry"},
		{goacodegen.NameFunction, "WithEndpoint"},
	}
	for _, name := range fixed {
		if err := pkg.DeclareName(goacodegen.NewExactName(name.kind, name.name)); err != nil {
			return nil, fmt.Errorf("plan registry %q name %q: %w", registry.Name, name.name, err)
		}
	}
	planned := &plannedRegistryClient{
		registry: registry,
		pkg:      pkg,
		security: make(map[*goaexpr.SchemeExpr]*plannedRegistrySecurity),
	}
	for _, requirement := range registry.Expr.Requirements {
		for _, scheme := range requirement.Schemes {
			if scheme.Kind == goaexpr.NoKind {
				continue
			}
			authType := goacodegen.NewPreferredName(
				goacodegen.NameType,
				goacodegen.Goify(scheme.SchemeName, true)+"Auth",
				goacodegen.ExportedName,
				registrySecurityNameOrder{
					service:  service.Name,
					registry: registry.Name,
					scheme:   scheme.SchemeName,
					use:      "type",
				},
			)
			if err := pkg.DeclareName(authType); err != nil {
				return nil, fmt.Errorf("plan registry %q security type: %w", registry.Name, err)
			}
			option := goacodegen.NewPreferredName(
				goacodegen.NameFunction,
				"With"+goacodegen.Goify(scheme.SchemeName, true),
				goacodegen.ExportedName,
				registrySecurityNameOrder{
					service:  service.Name,
					registry: registry.Name,
					scheme:   scheme.SchemeName,
					use:      "option",
				},
			)
			if err := pkg.DeclareName(option); err != nil {
				return nil, fmt.Errorf("plan registry %q security option: %w", registry.Name, err)
			}
			planned.security[scheme] = &plannedRegistrySecurity{authType: authType, option: option}
		}
	}
	return planned, nil
}

// registryClientPackageImports lists every package name written directly by
// the registry client templates. The package plan requires these names before
// generated authentication declarations are chosen.
func registryClientPackageImports() []*goacodegen.ImportSpec {
	return []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"),
		goacodegen.SimpleImport("encoding/json"),
		goacodegen.SimpleImport("errors"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("io"),
		goacodegen.SimpleImport("net/http"),
		goacodegen.SimpleImport("net/url"),
		goacodegen.SimpleImport("time"),
		goacodegen.NewImport("registry", "goa.design/goa-ai/runtime/registry"),
	}
}
