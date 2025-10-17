//nolint:lll // example patchers include long string replacements for clarity
package codegen

import (
	"fmt"
	"path/filepath"
	"strings"

	mcpexpr "goa.design/goa-ai/expr"
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

// ModifyExampleFiles augments main.go so handleHTTPServer signature and call include JSON-RPC args in correct order.
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
	files = patchExampleStubForMCP(mcpServices, files)

	for _, svr := range r.API.Servers {
		dir := example.Servers.Get(svr, r).Dir
		files = patchCLIForServer(dir, mcpServices, files)
		files = patchMainForServer(dir, mcpServices, files)
	}

	return files, nil
}

// cliParseReplacement is injected in place of the default cli.ParseEndpoint return.
const cliParseReplacement = `_, payload, err := cli.ParseEndpoint(
	scheme,
	host,
	doer,
	goahttp.RequestEncoder,
	goahttp.ResponseDecoder,
	debug,
)
if err != nil {
	return nil, nil, err
}
// MCP-backed adapter endpoints
e := mcpac.NewEndpoints(scheme, host, doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, debug)
// Extract non-flag args for service and subcommand
var nonflags []string
for i := 1; i < len(os.Args); i++ {
	a := os.Args[i]
	if strings.HasPrefix(a, "-") {
		if !strings.Contains(a, "=") && i+1 < len(os.Args) { i++ }
		continue
	}
	nonflags = append(nonflags, a)
}
if len(nonflags) < 2 {
	return nil, nil, fmt.Errorf("not enough arguments")
}
service := nonflags[0]
subcmd := nonflags[1]
switch service {
default:
	switch subcmd {
	case "analyze-text":
		return e.AnalyzeText, payload, nil
	case "search-knowledge":
		return e.SearchKnowledge, payload, nil
	case "execute-code":
		return e.ExecuteCode, payload, nil
	case "list-documents":
		return e.ListDocuments, payload, nil
	case "get-system-info":
		return e.GetSystemInfo, payload, nil
	case "generate-prompts":
		return e.GeneratePrompts, payload, nil
	case "send-notification":
		return e.SendNotification, payload, nil
	case "subscribe-to-updates":
		return e.SubscribeToUpdates, payload, nil
	case "process-batch":
		return e.ProcessBatch, payload, nil
	}
}
return nil, nil, fmt.Errorf("unknown service %q or command %q", service, subcmd)`

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
func patchCLIForServer(dir string, mcpServices []*expr.ServiceExpr, files []*codegen.File) []*codegen.File {
	cliPath := filepath.Join("cmd", dir+"-cli", "jsonrpc.go")
	var cliFile *codegen.File
	for _, f := range files {
		if f.Path != cliPath {
			continue
		}
		cliFile = f
		break
	}
	// Add imports in source header
	header := findSection(cliFile, headerSection)
	if header != nil {
		baseModule := deriveBaseModuleFromHeader(header)
		if baseModule != "" && len(mcpServices) > 0 {
			svcSnake := codegen.SnakeCase(mcpServices[0].Name)
			codegen.AddImport(header, &codegen.ImportSpec{Path: baseModule + "/gen/mcp_" + svcSnake + "/adapter/client", Name: "mcpac"})
		}
		codegen.AddImport(header,
			&codegen.ImportSpec{Path: "fmt"},
			&codegen.ImportSpec{Path: "os"},
			&codegen.ImportSpec{Path: "strings"},
		)
	}
	// Replace the endpoint constructor logic
	for _, s := range cliFile.SectionTemplates {
		idx := strings.Index(s.Source, "return cli.ParseEndpoint(")
		if idx < 0 {
			continue
		}
		endrel := strings.Index(s.Source[idx:], ")\n")
		if endrel < 0 {
			continue
		}
		end := idx + endrel + 2
		s.Source = s.Source[:idx] + cliParseReplacement + s.Source[end:]
	}
	return files
}

// patchMainForServer updates cmd/<dir>/main.go to wire the MCP adapter service
// instead of the stub implementation. We look for assignment to the MCP service
// variable (e.g., mcpAssistantSvc) and replace the RHS with adapter constructor.
func patchMainForServer(dir string, mcpServices []*expr.ServiceExpr, files []*codegen.File) []*codegen.File {
	if len(mcpServices) == 0 {
		return files
	}
	mainPath := filepath.Join("cmd", dir, "main.go")
	var fmain *codegen.File
	for _, f := range files {
		if f.Path == mainPath {
			fmain = f
			break
		}
	}
	if fmain == nil {
		return files
	}

	// Determine MCP package alias from existing imports; fallback to generated alias
	header := findSection(fmain, headerSection)
	mcpAlias := ""
	if header != nil {
		if data, ok := header.Data.(map[string]any); ok {
			if imv, ok2 := data["Imports"]; ok2 {
				if specs, ok3 := imv.([]*codegen.ImportSpec); ok3 {
					for _, spec := range specs {
						if strings.Contains(spec.Path, "/gen/mcp_") {
							mcpAlias = spec.Name
							break
						}
					}
				}
			}
		}
	}
	if mcpAlias == "" {
		mcpAlias = codegen.Goify("mcp_"+codegen.SnakeCase(mcpServices[0].Name), false)
	}

	// Build variable root for MCP service variable name
	varRoot := codegen.Goify("mcp_"+codegen.SnakeCase(mcpServices[0].Name), false)
	mcpSvcVar := varRoot + "Svc"

	// Replace assignment to mcp<Service>Svc with adapter constructor
	// and also replace factory NewMcp<Service>() calls as a fallback
	for _, s := range fmain.SectionTemplates {
		if s.Name == headerSection {
			continue
		}
		src := s.Source
		// Primary: variable assignment replacement
		if strings.Contains(src, mcpSvcVar+" =") {
			lines := strings.Split(src, "\n")
			var b strings.Builder
			changed := false
			for _, ln := range lines {
				idx := strings.Index(ln, mcpSvcVar+" =")
				if idx >= 0 {
					lead := ln[:idx]
					b.WriteString(lead)
					b.WriteString(mcpSvcVar + " = " + mcpAlias + ".NewMCPAdapter(assistantSvc, nil, nil)")
					b.WriteByte('\n')
					changed = true
					continue
				}
				b.WriteString(ln)
				b.WriteByte('\n')
			}
			if changed {
				s.Source = b.String()
				continue
			}
		}
		// Fallback: replace factory constructor call of the stub
		svcGo := codegen.Goify(mcpServices[0].Name, true)
		factory := "assistantapi.NewMcp" + svcGo + "()"
		if strings.Contains(src, factory) {
			s.Source = strings.ReplaceAll(src, factory, mcpAlias+".NewMCPAdapter(assistantSvc, nil, nil)")
		}
	}
	return files
}

// patchExampleStubForMCP rewrites the generated example stub (mcp_<svc>.go)
// to return the adapter that wraps the original service implementation, so the
// server exposes proper MCP behavior.
func patchExampleStubForMCP(mcpServices []*expr.ServiceExpr, files []*codegen.File) []*codegen.File {
	if len(mcpServices) == 0 {
		return files
	}

	// Patch any generated top-level MCP stub: func NewMcp<Service>() should
	// return the adapter wrapping the original service (New<Service>()).
	svc := mcpServices[0]
	svcGo := codegen.Goify(svc.Name, true) // e.g., Assistant
	// We expect stub file path to be mcp_<svc>.go at repository root (as produced by goa example)
	for _, f := range files {
		// Heuristic: look for a function named NewMcp<Service>
		hasFactory := false
		for _, s := range f.SectionTemplates {
			if s.Name == headerSection {
				continue
			}
			if strings.Contains(s.Source, "func NewMcp"+svcGo+"()") {
				hasFactory = true
				break
			}
		}
		if !hasFactory {
			continue
		}

		// Determine alias of MCP package import from header
		header := findSection(f, headerSection)
		mcpAlias := ""
		if header != nil {
			if data, ok := header.Data.(map[string]any); ok {
				if imv, ok2 := data["Imports"]; ok2 {
					if specs, ok3 := imv.([]*codegen.ImportSpec); ok3 {
						for _, spec := range specs {
							if strings.Contains(spec.Path, "/gen/mcp_") {
								mcpAlias = spec.Name
								break
							}
						}
					}
				}
			}
		}
		if mcpAlias == "" {
			mcpAlias = codegen.Goify("mcp_"+codegen.SnakeCase(svc.Name), false)
		}

		// Perform replacement inside factory: return &mcp<Go>srvc{} -> return <mcpAlias>.NewMCPAdapter(New<Service>(), nil, nil)
		repl := mcpAlias + ".NewMCPAdapter(New" + svcGo + "(), nil, nil)"
		for _, s := range f.SectionTemplates {
			if s.Name == headerSection {
				continue
			}
			// Known example stub return; also fallback to generic pattern
			if strings.Contains(s.Source, "return &mcp"+svcGo+"srvc{}") {
				s.Source = strings.Replace(s.Source, "return &mcp"+svcGo+"srvc{}", "return "+repl, 1)
				continue
			}
			if strings.Contains(s.Source, "return &mcp") && strings.Contains(s.Source, "srvc{}") {
				// Last resort: coarse replacement
				s.Source = strings.Replace(s.Source, "srvc{}", ")", 1)
				s.Source = strings.Replace(s.Source, "return &mcp", "return "+repl, 1)
			}
		}
		// Only patch the first matching file
		break
	}
	return files
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
