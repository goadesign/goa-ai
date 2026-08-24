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
		byService map[string][]*plannedRegistryClient
	}

	// plannedRegistryClient stores one registry definition and its generated
	// package names.
	plannedRegistryClient struct {
		registry *agentir.Registry
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
		if len(service.Agents) == 0 && len(service.Completions) == 0 {
			continue
		}
		for _, registry := range design.Registries {
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
	for _, service := range data.Services {
		for _, planned := range p.byService[service.Service.Name] {
			service.RegistryClients = append(service.RegistryClients, newRegistryClientData(
				data.Genpkg,
				service.Service.PathName,
				planned.registry.Expr,
				planned,
			))
		}
	}
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
