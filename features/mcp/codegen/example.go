//nolint:lll // example patchers include long string replacements for clarity
package codegen

import (
	"fmt"
	"path/filepath"
	"strings"

	mcpexpr "goa.design/goa-ai/features/mcp/expr"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/example"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// PrepareExample augments the original roots so the Goa example generator includes the
// MCP JSON-RPC server without manual cmd edits.
func PrepareExample(_ string, roots []eval.Root) error {
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
			builder := newMCPExprBuilder(svc, mcp)
			mcpService := builder.BuildServiceExpr()

			// Capture original JSON-RPC path (used by builder)
			if _, exists := getOriginalJSONRPCPath(svc.Name); !exists && r.API != nil && r.API.JSONRPC != nil {
				for _, jsonrpcSvc := range r.API.JSONRPC.Services {
					if jsonrpcSvc.ServiceExpr == nil || jsonrpcSvc.ServiceExpr.Name != svc.Name {
						continue
					}
					if route := jsonrpcSvc.JSONRPCRoute; route != nil && route.Path != "" {
						setOriginalJSONRPCPath(svc.Name, route.Path)
					}
					break
				}
			}

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
			// Ensure existing servers reference MCP service for example wiring
			for _, srv := range r.API.Servers {
				if !stringInList(srv.Services, mcpService.Name) {
					srv.Services = append(srv.Services, mcpService.Name)
				}
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
	files = generateExampleAdapterStubs(mcpServices, files)

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
func generateExampleAdapterStubs(mcpServices []*expr.ServiceExpr, files []*codegen.File) []*codegen.File {
	if len(mcpServices) == 0 {
		return files
	}
	// Build lookup of files by path for quick replacement
	byPath := make(map[string]*codegen.File, len(files))
	for _, f := range files {
		byPath[filepath.ToSlash(f.Path)] = f
	}
	for _, svc := range mcpServices {
		svcGo := codegen.Goify(svc.Name, true)
		svcSnake := codegen.SnakeCase(svc.Name)
		// Locate existing stub file and header to copy package/import context
		stubPath := filepath.ToSlash("mcp_" + svcSnake + ".go")
		f := byPath[stubPath]
		if f == nil {
			// As a fallback, scan for any file declaring NewMcp<Service>()
			for _, cf := range files {
				for _, s := range cf.SectionTemplates {
					if s.Name != headerSection && strings.Contains(s.Source, "func NewMcp"+svcGo+"()") {
						f = cf
						break
					}
				}
				if f != nil {
					break
				}
			}
		}
		if f == nil {
			// No stub found; skip
			continue
		}
		header := findSection(f, headerSection)
		if header == nil {
			continue
		}
		// Ensure import for MCP service package exists and capture its alias
		mcpAlias := ""
		if data, ok := header.Data.(map[string]any); ok {
			if imv, ok2 := data["Imports"]; ok2 {
				if specs, ok3 := imv.([]*codegen.ImportSpec); ok3 {
					for _, spec := range specs {
						if strings.HasSuffix(spec.Path, "/gen/mcp_"+svcSnake) {
							mcpAlias = spec.Name
							break
						}
					}
				}
			}
		}
		if mcpAlias == "" {
			// Derive base module from any CLI header if available
			base := deriveBaseModuleFromFiles(files)
			if base != "" {
				codegen.AddImport(header, &codegen.ImportSpec{Path: base + "/gen/mcp_" + svcSnake, Name: codegen.Goify("mcp_"+svcSnake, false)})
				mcpAlias = codegen.Goify("mcp_"+svcSnake, false)
			}
		}
		if mcpAlias == "" {
			// As a last resort keep using a conventional alias
			mcpAlias = codegen.Goify("mcp_"+svcSnake, false)
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
	return files
}

// deriveBaseModuleFromFiles attempts to locate the module import prefix by inspecting
// CLI files that import gen/jsonrpc/cli/. Returns empty string if not found.
func deriveBaseModuleFromFiles(files []*codegen.File) string {
	for _, f := range files {
		p := filepath.ToSlash(f.Path)
		if !strings.Contains(p, "/cmd/") || !strings.HasSuffix(p, "/jsonrpc.go") {
			continue
		}
		header := findSection(f, headerSection)
		if header == nil {
			continue
		}
		if base := deriveBaseModuleFromHeader(header); base != "" {
			return base
		}
	}
	return ""
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

// PatchCLIToUseMCPAdapter rewrites the generated service CLI support code to instantiate
// the MCP adapter client instead of the original JSON-RPC service client.
func PatchCLIToUseMCPAdapter(_ string, _ []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	for _, f := range files {
		// Target generated CLI support under gen/jsonrpc/cli/<service>/cli.go
		p := filepath.ToSlash(f.Path)
		if !strings.Contains(p, "/jsonrpc/cli/") || !strings.HasSuffix(p, "/cli.go") {
			continue
		}
		// Extract service snake from path segment after /jsonrpc/cli/
		svc := ""
		if idx := strings.Index(p, "/jsonrpc/cli/"); idx >= 0 {
			rest := p[idx+len("/jsonrpc/cli/"):]
			if j := strings.Index(rest, "/"); j > 0 {
				svc = rest[:j]
			}
		}
		if svc == "" {
			continue
		}
		// 1) Find header and add adapter import via AddImport
		var header *codegen.SectionTemplate
		for _, s := range f.SectionTemplates {
			if s.Name == "source-header" {
				header = s
				break
			}
		}
		if header == nil {
			return files, fmt.Errorf("header not found in %s", f.Path)
		}
		var origAlias string
		// Inspect existing imports to locate the original JSON-RPC client import
		var imps []*codegen.ImportSpec
		if data, ok := header.Data.(map[string]any); ok {
			if imv, ok2 := data["Imports"]; ok2 {
				if specs, ok3 := imv.([]*codegen.ImportSpec); ok3 {
					imps = specs
				}
			}
		}
		for _, spec := range imps {
			if strings.HasSuffix(spec.Path, "/gen/jsonrpc/"+svc+"/client") {
				baseModule := strings.TrimSuffix(spec.Path, "/gen/jsonrpc/"+svc+"/client")
				adapterPath := baseModule + "/gen/mcp_" + svc + "/adapter/client"
				codegen.AddImport(header, &codegen.ImportSpec{Path: adapterPath, Name: "mcpac"})
				origAlias = spec.Name
				break
			}
		}
		// 2) Replace constructor call to use the adapter client
		if origAlias == "" {
			// Fallback to conventional alias used by Goa: <service> + "c"
			origAlias = svc + "c"
		}
		if origAlias != "" {
			for _, s := range f.SectionTemplates {
				if s.Name == "source-header" {
					continue
				}
				s.Source = strings.ReplaceAll(s.Source, "c := "+origAlias+".NewClient(", "c := mcpac.NewClient(")
			}
		}
	}
	return files, nil
}
