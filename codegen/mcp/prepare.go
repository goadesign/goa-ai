// Package codegen adds the generated MCP services and protocol types to Goa's
// evaluated design before Goa chooses package, type, and function names.
package codegen

import (
	"fmt"
	"slices"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// prepareMCPServices adds every generated MCP service to the same Goa design
// as its user service. It returns the services it added.
func prepareMCPServices(roots []eval.Root) ([]*preparedMCPService, error) {
	mcpRoot, err := findMCPRoot(roots)
	if err != nil {
		return nil, err
	}
	return prepareMCPServicesFromRoot(roots, mcpRoot)
}

// prepareMCPServicesFromRoot adds services using the MCP root selected for this run.
func prepareMCPServicesFromRoot(
	roots []eval.Root,
	mcpRoot *mcpexpr.RootExpr,
) ([]*preparedMCPService, error) {
	source := collectSourceSnapshot(roots)
	var prepared []*preparedMCPService
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}
		services := append([]*expr.ServiceExpr(nil), r.Services...)
		generatedTypes := make(map[string]*expr.UserTypeExpr)
		usedTypeNames := make(map[string]struct{}, len(r.Types))
		for _, userType := range r.Types {
			usedTypeNames[userType.Name()] = struct{}{}
		}
		var attachedServices []*expr.ServiceExpr
		var attachedTypes []expr.UserType
		for _, svc := range services {
			if !mcpRoot.HasMCP(svc) {
				continue
			}
			mcp := mcpRoot.GetMCP(svc)
			if err := validateMCPService(svc, mcp); err != nil {
				return nil, err
			}

			builder := newMCPExprBuilder(svc, mcp)
			for name, userType := range generatedTypes {
				builder.Types()[name] = userType
			}
			mcpService := builder.BuildServiceExpr()
			for _, server := range r.API.Servers {
				if slices.Contains(server.Services, svc.Name) &&
					!slices.Contains(server.Services, mcpService.Name) {
					server.Services = append(server.Services, mcpService.Name)
				}
			}
			_, protocolTypes := builder.Attach(r, mcpService, source.jsonrpcPaths[svc.Name])

			for _, userType := range protocolTypes {
				name := userType.Name()
				if _, ok := generatedTypes[name]; ok {
					continue
				}
				if _, ok := usedTypeNames[name]; ok {
					return nil, fmt.Errorf("MCP type %q conflicts with a Goa type", name)
				}
				generated := userType.(*expr.UserTypeExpr)
				generatedTypes[name] = generated
				usedTypeNames[name] = struct{}{}
				r.Types = append(r.Types, generated)
				attachedTypes = append(attachedTypes, generated)
			}
			attachedServices = append(attachedServices, mcpService)
			prepared = append(prepared, &preparedMCPService{
				root:        r,
				userService: svc,
				mcpService:  mcpService,
				mcp:         mcp,
			})
		}
		if len(attachedServices) > 0 {
			if err := r.EvaluateAttachedServices(attachedServices, attachedTypes...); err != nil {
				return nil, fmt.Errorf("prepare MCP services: %w", err)
			}
		}
	}
	return prepared, nil
}

// findMCPRoot returns the one MCP root evaluated for this generation run.
func findMCPRoot(roots []eval.Root) (*mcpexpr.RootExpr, error) {
	var found *mcpexpr.RootExpr
	for _, root := range roots {
		mcpRoot, ok := root.(*mcpexpr.RootExpr)
		if !ok {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("generation roots contain more than one MCP root")
		}
		found = mcpRoot
	}
	if found == nil {
		return nil, fmt.Errorf("generation roots do not contain an MCP root")
	}
	return found, nil
}
