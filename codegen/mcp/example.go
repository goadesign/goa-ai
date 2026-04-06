//nolint:lll // example patchers include long string replacements for clarity
package codegen

import (
	"fmt"
	"path/filepath"
	"strings"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/example"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// PrepareExample augments the original roots so the Goa example generator
// includes the MCP JSON-RPC server without manual cmd edits. It runs the same
// pure-MCP contract validation as Generate so example scaffolding cannot mask
// invalid MCP mappings.
func PrepareExample(_ string, roots []eval.Root) error {
	source := collectSourceSnapshot(roots)
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}
		for _, svc := range r.Services {
			if !mcpexpr.Root.HasMCP(svc) {
				continue
			}
			mcp := mcpexpr.Root.GetMCP(svc)
			if err := validatePureMCPService(svc, mcp, source); err != nil {
				return err
			}
			builder := newMCPExprBuilder(svc, mcp, source)
			mcpService := builder.BuildServiceExpr()

			// Build and validate a temporary MCP root to finalize types
			mcpTempRoot := builder.BuildRootExpr(mcpService)
			if err := builder.PrepareAndValidate(mcpTempRoot); err != nil {
				return err
			}

			// Inject MCP into original root so example generation mounts it
			if r.API == nil {
				r.API = &expr.APIExpr{}
			}
			if r.API.HTTP == nil {
				r.API.HTTP = &expr.HTTPExpr{}
			}
			httpSvc := builder.buildHTTPService(mcpService)
			httpSvc.Root = r.API.HTTP
			if r.API.JSONRPC == nil {
				r.API.JSONRPC = &expr.JSONRPCExpr{}
			}

			// Remove original HTTP/JSON-RPC services for MCP-enabled service from the example
			{
				// HTTP
				if len(r.API.HTTP.Services) > 0 {
					filtered := make([]*expr.HTTPServiceExpr, 0, len(r.API.HTTP.Services))
					for _, hs := range r.API.HTTP.Services {
						if hs.ServiceExpr != nil && hs.ServiceExpr.Name == svc.Name {
							continue
						}
						filtered = append(filtered, hs)
					}
					r.API.HTTP.Services = filtered
				}
				// JSON-RPC
				if len(r.API.JSONRPC.Services) > 0 {
					filtered := make([]*expr.HTTPServiceExpr, 0, len(r.API.JSONRPC.Services))
					for _, js := range r.API.JSONRPC.Services {
						if js.ServiceExpr != nil && js.ServiceExpr.Name == svc.Name {
							continue
						}
						filtered = append(filtered, js)
					}
					r.API.JSONRPC.Services = filtered
				}
			}
			// Add to JSONRPC.HTTP services if not already present
			already := false
			for _, hs := range r.API.JSONRPC.Services {
				if hs.ServiceExpr != nil && hs.ServiceExpr.Name == httpSvc.ServiceExpr.Name {
					already = true
					break
				}
			}
			if !already {
				r.API.JSONRPC.Services = append(r.API.JSONRPC.Services, httpSvc)
			}
			// Remove original JSON-RPC service for this server so /rpc is handled by MCP only
			if len(r.API.JSONRPC.Services) > 0 {
				filtered := make([]*expr.HTTPServiceExpr, 0, len(r.API.JSONRPC.Services))
				for _, js := range r.API.JSONRPC.Services {
					if js.ServiceExpr != nil && js.ServiceExpr.Name == svc.Name {
						continue
					}
					filtered = append(filtered, js)
				}
				r.API.JSONRPC.Services = filtered
			}
			// Add MCP service once to JSONRPC services
			present := false
			for _, js := range r.API.JSONRPC.Services {
				if js.ServiceExpr != nil && js.ServiceExpr.Name == httpSvc.ServiceExpr.Name {
					present = true
					break
				}
			}
			if !present {
				r.API.JSONRPC.Services = append(r.API.JSONRPC.Services, httpSvc)
			}
			// Add mcp service once to top-level services
			if !serviceInList(r.Services, mcpService.Name) {
				r.Services = append(r.Services, mcpService)
			}
			// Only mount the MCP service on servers that originally exposed the
			// source service. Example generation should preserve server ownership.
			for _, srv := range r.API.Servers {
				if !stringInList(srv.Services, svc.Name) || stringInList(srv.Services, mcpService.Name) {
					continue
				}
				srv.Services = append(srv.Services, mcpService.Name)
			}
		}
	}
	return nil
}

// ModifyExampleFiles patches example CLI wiring to target the MCP adapter client
// and replaces the default MCP stub factory to return the adapter-wrapped
// service. It avoids touching HTTP server signatures or example mains.
func ModifyExampleFiles(_ string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	r, ok := firstRootWithJSONRPC(roots)
	if !ok {
		return files, nil
	}

	mcpServices := collectMCPServices(r)
	if len(mcpServices) == 0 {
		return files, nil
	}

	// Ensure example stub returns the adapter-backed service instead of zero-value stub
	files, err := generateExampleAdapterStubs(mcpServices, files)
	if err != nil {
		return nil, err
	}

	for _, svr := range r.API.Servers {
		dir := example.Servers.Get(svr, r).Dir
		files = patchCLIForServer(dir, svr, mcpServices, files)
	}

	return files, nil
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
func collectMCPServices(r *expr.RootExpr) []*expr.ServiceExpr {
	var svcs []*expr.ServiceExpr
	for _, sv := range r.Services {
		if mcpexpr.Root.HasMCP(sv) {
			svcs = append(svcs, sv)
		}
	}
	return svcs
}

// patchCLIForServer locates the generated JSON-RPC CLI support file and rewrites it
// to instantiate the MCP adapter client endpoints. It also adds required imports.
func patchCLIForServer(dir string, svr *expr.ServerExpr, mcpServices []*expr.ServiceExpr, files []*codegen.File) []*codegen.File {
	cliPath := filepath.Join("cmd", dir+"-cli", "jsonrpc.go")
	var cliFile *codegen.File
	for _, f := range files {
		if f.Path != cliPath {
			continue
		}
		cliFile = f
		break
	}
	if cliFile == nil {
		return files
	}

	svcMap := make(map[string]*expr.ServiceExpr, len(mcpServices))
	for _, svc := range mcpServices {
		svcMap[svc.Name] = svc
	}

	var targetSvcs []*expr.ServiceExpr
	for _, name := range svr.Services {
		if svc := svcMap[name]; svc != nil {
			targetSvcs = append(targetSvcs, svc)
		}
	}
	if len(targetSvcs) == 0 {
		return files
	}

	header := findSection(cliFile, headerSection)
	if header == nil {
		return files
	}
	baseModule := deriveBaseModuleFromHeader(header)
	if baseModule == "" {
		return files
	}

	serviceData := buildCLIServiceData(targetSvcs, header, baseModule)
	if len(serviceData) == 0 {
		return files
	}
	replacement := renderCLIDoJSONRPC(serviceData)
	if replacement == "" {
		return files
	}

	codegen.AddImport(header,
		&codegen.ImportSpec{Path: "fmt"},
		&codegen.ImportSpec{Path: "os"},
		&codegen.ImportSpec{Path: "strings"},
	)

	section := findSectionByName(cliFile, "cli-http-start")
	if section != nil {
		section.Source = replacement
	}
	if end := findSectionByName(cliFile, "cli-http-end"); end != nil {
		end.Source = ""
	}
	return files
}

// patchExampleStubForMCP rewrites the generated example stub (mcp_<svc>.go)
// to return the adapter that wraps the original service implementation, so the
// server exposes proper MCP behavior.
func generateExampleAdapterStubs(mcpServices []*expr.ServiceExpr, files []*codegen.File) ([]*codegen.File, error) {
	if len(mcpServices) == 0 {
		return files, nil
	}
	// Build lookup of files by path for quick replacement
	byPath := make(map[string]*codegen.File, len(files))
	for _, f := range files {
		byPath[filepath.ToSlash(f.Path)] = f
	}
	for _, svc := range mcpServices {
		svcGo := codegen.Goify(svc.Name, true)
		stubPath := expectedExampleStubPath(svc)
		f := byPath[stubPath]
		if f == nil {
			return nil, fmt.Errorf("expected MCP example stub %q for service %q", stubPath, svc.Name)
		}
		header := findSection(f, headerSection)
		if header == nil {
			return nil, fmt.Errorf("example stub %q for service %q is missing %q", f.Path, svc.Name, headerSection)
		}
		mcpAlias, err := exampleStubImportAlias(header, svc)
		if err != nil {
			return nil, err
		}
		// Determine whether prompts are enabled to decide constructor arity
		hasPrompts := false
		if m := mcpexpr.Root.GetMCP(svc); m != nil {
			hasPrompts = m.Capabilities != nil && m.Capabilities.EnablePrompts
		}
		body := mcpTemplates.MustRender("example_mcp_stub", map[string]any{
			"ServiceGo":  svcGo,
			"MCPAlias":   mcpAlias,
			"HasPrompts": hasPrompts,
		})
		// Replace file content except header with our body
		f.SectionTemplates = []*codegen.SectionTemplate{header, {Name: exampleMCPStubSection, Source: body}}
	}
	return files, nil
}

// expectedExampleStubPath returns the canonical example stub file path emitted by
// Goa for one MCP-enabled service.
func expectedExampleStubPath(svc *expr.ServiceExpr) string {
	return filepath.ToSlash("mcp_" + codegen.SnakeCase(svc.Name) + ".go")
}

// exampleStubImportAlias returns the explicit import alias for the generated MCP
// package expected by the example stub header. The stub must already carry this
// import; example rewriting no longer invents one.
func exampleStubImportAlias(header *codegen.SectionTemplate, svc *expr.ServiceExpr) (string, error) {
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

// deriveBaseModuleFromHeader inspects the header imports to find the module path
// prefix used by the generated example code (by locating the JSON-RPC CLI import).
func deriveBaseModuleFromHeader(header *codegen.SectionTemplate) string {
	if header == nil || header.Data == nil {
		return ""
	}
	data, ok := header.Data.(map[string]any)
	if !ok {
		return ""
	}
	imv, ok := data["Imports"]
	if !ok {
		return ""
	}
	specs, ok := imv.([]*codegen.ImportSpec)
	if !ok {
		return ""
	}
	for _, spec := range specs {
		idx := strings.Index(spec.Path, "/gen/jsonrpc/cli/")
		if idx >= 0 {
			return spec.Path[:idx]
		}
	}
	return ""
}

func serviceInList(list []*expr.ServiceExpr, name string) bool {
	for _, s := range list {
		if s.Name == name {
			return true
		}
	}
	return false
}

func stringInList(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}

func findSectionByName(f *codegen.File, name string) *codegen.SectionTemplate {
	for _, s := range f.SectionTemplates {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func buildCLIServiceData(
	services []*expr.ServiceExpr,
	header *codegen.SectionTemplate,
	baseModule string,
) []cliServiceTemplateData {
	data := make([]cliServiceTemplateData, 0, len(services))
	for _, svc := range services {
		if len(svc.Methods) == 0 {
			continue
		}
		svcSnake := codegen.SnakeCase(svc.Name)
		alias := codegen.Goify("mcp_"+svcSnake, false) + "adapter"
		path := baseModule + "/gen/mcp_" + svcSnake + "/adapter/client"
		codegen.AddImport(header, &codegen.ImportSpec{Path: path, Name: alias})

		methods := make([]cliMethodTemplateData, 0, len(svc.Methods))
		for _, m := range svc.Methods {
			methods = append(methods, cliMethodTemplateData{
				Command:  methodCommandName(m.Name),
				Endpoint: codegen.Goify(m.Name, true),
			})
		}
		data = append(data, cliServiceTemplateData{
			Name:    svc.Name,
			Alias:   alias,
			Methods: methods,
		})
	}
	return data
}

func renderCLIDoJSONRPC(services []cliServiceTemplateData) string {
	if len(services) == 0 {
		return ""
	}
	data := cliParseTemplateData{Services: services}
	return mcpTemplates.MustRender("cli_dojsonrpc", data)
}

func methodCommandName(name string) string {
	return strings.ReplaceAll(codegen.SnakeCase(name), "_", "-")
}
