// Package codegen connects generated MCP services to Goa's example server.
package codegen

import (
	"fmt"
	"path/filepath"
	"strings"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	goagenerator "goa.design/goa/v3/codegen/generator"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// mcpExamplePlugin keeps the roots selected for one example command.
	mcpExamplePlugin struct {
		mcpRoot     *mcpexpr.RootExpr
		exampleRoot *expr.RootExpr
	}

	// exampleMCPService stores one user service and whether its MCP server has prompts.
	exampleMCPService struct {
		service             *expr.ServiceExpr
		hasPrompts          bool
		stubPath            string
		mcpPackagePath      string
		mcpConstructorName  string
		userConstructorName string
		mcpServiceInterface string
	}
)

// newMCPExamplePlugin returns a plugin whose Prepare and Generate methods share
// a new mcpExamplePlugin for one command.
func newMCPExamplePlugin() goagenerator.Plugin {
	plugin := new(mcpExamplePlugin)
	return goagenerator.Plugin{
		Prepare:  plugin.prepare,
		Generate: plugin.generate,
	}
}

// prepare selects this run's roots and prepares the generated MCP services.
func (p *mcpExamplePlugin) prepare(_ string, roots []eval.Root) error {
	mcpRoot, err := findMCPRoot(roots)
	if err != nil {
		return err
	}
	p.mcpRoot = mcpRoot
	p.exampleRoot, _ = firstRootWithJSONRPC(roots)
	_, err = prepareMCPServicesFromRoot(roots, mcpRoot)
	return err
}

// generate makes the example server return the MCP service backed by the user
// service.
func (p *mcpExamplePlugin) generate(
	plan *goagenerator.Plan,
	files []*codegen.File,
) ([]*codegen.File, error) {
	if p.mcpRoot == nil {
		return nil, fmt.Errorf("MCP example generation was not prepared")
	}
	if p.exampleRoot == nil {
		return files, nil
	}

	mcpServices := collectMCPServices(p.exampleRoot, p.mcpRoot)
	if len(mcpServices) == 0 {
		return files, nil
	}

	if err := bindExampleMCPServices(plan, p.exampleRoot, mcpServices); err != nil {
		return nil, err
	}
	files, err := generateExampleAdapterStubs(mcpServices, files)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// bindExampleMCPServices copies the final constructor, interface, package, and
// file names chosen by Goa for each example stub.
func bindExampleMCPServices(
	plan *goagenerator.Plan,
	root *expr.RootExpr,
	services []exampleMCPService,
) error {
	planned := plan.Service(root).Services()
	for index := range services {
		service := &services[index]
		user := planned.Get(service.service.Name)
		if user == nil {
			return fmt.Errorf("goa did not plan example service %q", service.service.Name)
		}
		mcp := planned.Get("mcp_" + service.service.Name)
		if mcp == nil {
			return fmt.Errorf("goa did not plan MCP example service %q", service.service.Name)
		}
		service.stubPath = filepath.ToSlash(mcp.PathName + ".go")
		service.mcpPackagePath = mcp.PathName
		service.mcpConstructorName = mcp.ExampleConstructorDeclaration.Name()
		service.userConstructorName = user.ExampleConstructorDeclaration.Name()
		service.mcpServiceInterface = mcp.ServiceDeclaration.Name()
	}
	return nil
}

// firstRootWithJSONRPC returns the first root with JSON-RPC configured.
func firstRootWithJSONRPC(roots []eval.Root) (*expr.RootExpr, bool) {
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok || r.API == nil || r.API.JSONRPC == nil {
			continue
		}
		return r, true
	}
	return nil, false
}

// collectMCPServices returns services that have MCP configured in DSL.
func collectMCPServices(r *expr.RootExpr, mcpRoot *mcpexpr.RootExpr) []exampleMCPService {
	var services []exampleMCPService
	for _, sv := range r.Services {
		mcp := mcpRoot.GetMCP(sv)
		if mcp != nil {
			services = append(services, exampleMCPService{
				service:    sv,
				hasPrompts: mcp.Capabilities != nil && mcp.Capabilities.EnablePrompts,
			})
		}
	}
	return services
}

// generateExampleAdapterStubs changes each MCP example constructor to return a
// service backed by the matching user service.
func generateExampleAdapterStubs(
	mcpServices []exampleMCPService,
	files []*codegen.File,
) ([]*codegen.File, error) {
	if len(mcpServices) == 0 {
		return files, nil
	}
	// Build lookup of files by path for quick replacement
	byPath := make(map[string]*codegen.File, len(files))
	for _, f := range files {
		byPath[filepath.ToSlash(f.Path)] = f
	}
	for _, mcpService := range mcpServices {
		svc := mcpService.service
		stubPath := expectedExampleStubPath(svc)
		if mcpService.stubPath != "" {
			stubPath = mcpService.stubPath
		}
		f := byPath[stubPath]
		if f == nil {
			return nil, fmt.Errorf("expected MCP example stub %q for service %q", stubPath, svc.Name)
		}
		header := findSection(f, headerSection)
		if header == nil {
			return nil, fmt.Errorf("example stub %q for service %q is missing %q", f.Path, svc.Name, headerSection)
		}
		mcpAlias, err := exampleStubImportAlias(header, mcpService)
		if err != nil {
			return nil, err
		}
		body := mcpTemplates.MustRender("example_mcp_stub", map[string]any{
			"MCPConstructorName":  mcpService.mcpConstructorName,
			"UserConstructorName": mcpService.userConstructorName,
			"MCPServiceInterface": mcpService.mcpServiceInterface,
			"MCPAlias":            mcpAlias,
			"HasPrompts":          mcpService.hasPrompts,
		})
		// Replace file content except header with our body
		f.SectionTemplates = []*codegen.SectionTemplate{header, {Name: exampleMCPStubSection, Source: body}}
	}
	return files, nil
}

// expectedExampleStubPath returns the example filename Goa writes for one MCP service.
func expectedExampleStubPath(svc *expr.ServiceExpr) string {
	return filepath.ToSlash("mcp_" + codegen.SnakeCase(svc.Name) + ".go")
}

// exampleStubImportAlias returns the name used to refer to the generated MCP
// package. The example file must already import that package.
func exampleStubImportAlias(header *codegen.SectionTemplate, service exampleMCPService) (string, error) {
	svc := service.service
	if header == nil || header.Data == nil {
		return "", fmt.Errorf("example stub %q is missing import metadata", expectedExampleStubPath(svc))
	}
	data, ok := header.Data.(map[string]any)
	if !ok {
		return "", fmt.Errorf("example stub %q has unexpected header metadata", expectedExampleStubPath(svc))
	}
	imv, ok := data["Imports"]
	if !ok {
		return "", fmt.Errorf("example stub %q is missing imports", expectedExampleStubPath(svc))
	}
	specs, ok := imv.([]*codegen.ImportSpec)
	if !ok {
		return "", fmt.Errorf("example stub %q has unexpected imports metadata", expectedExampleStubPath(svc))
	}
	wantSuffix := "/gen/mcp_" + codegen.SnakeCase(svc.Name)
	if service.mcpPackagePath != "" {
		wantSuffix = "/gen/" + service.mcpPackagePath
	}
	for _, spec := range specs {
		if !strings.HasSuffix(spec.Path, wantSuffix) {
			continue
		}
		if spec.Name == "" {
			return "", fmt.Errorf(
				"example stub %q must import %q with an explicit alias",
				expectedExampleStubPath(svc),
				spec.Path,
			)
		}
		return spec.Name, nil
	}
	return "", fmt.Errorf(
		"example stub %q must import generated MCP package with suffix %q",
		expectedExampleStubPath(svc),
		wantSuffix,
	)
}

// findSection returns the first section with the given name in file f.
func findSection(f *codegen.File, name string) *codegen.SectionTemplate {
	for _, s := range f.SectionTemplates {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func removeHTTPServiceByName(services []*expr.HTTPServiceExpr, name string) []*expr.HTTPServiceExpr {
	if len(services) == 0 {
		return nil
	}
	filtered := make([]*expr.HTTPServiceExpr, 0, len(services))
	for _, svc := range services {
		if svc != nil && svc.ServiceExpr != nil && svc.ServiceExpr.Name == name {
			continue
		}
		filtered = append(filtered, svc)
	}
	return filtered
}

func replaceHTTPServiceByName(services []*expr.HTTPServiceExpr, removeName string, replacement *expr.HTTPServiceExpr) []*expr.HTTPServiceExpr {
	replacementName := ""
	if replacement != nil && replacement.ServiceExpr != nil {
		replacementName = replacement.ServiceExpr.Name
	}
	filtered := make([]*expr.HTTPServiceExpr, 0, len(services)+1)
	for _, svc := range services {
		if svc != nil && svc.ServiceExpr != nil {
			name := svc.ServiceExpr.Name
			if name == removeName || (replacementName != "" && name == replacementName) {
				continue
			}
		}
		filtered = append(filtered, svc)
	}
	if replacement != nil {
		filtered = append(filtered, replacement)
	}
	return filtered
}
