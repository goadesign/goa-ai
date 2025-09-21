package codegen

import (
	"path/filepath"
	"strings"

	mcpexpr "goa.design/goa-ai/expr"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/example"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
	httpcodegen "goa.design/goa/v3/http/codegen"
)

// PrepareExample augments the original roots so the Goa example generator includes the
// MCP JSON-RPC server without manual cmd edits.
func PrepareExample(genpkg string, roots []eval.Root) error {
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
			for _, hs := range r.API.JSONRPC.HTTPExpr.Services {
				if hs.ServiceExpr != nil && hs.ServiceExpr.Name == httpSvc.ServiceExpr.Name {
					already = true
					break
				}
			}
			if !already {
				r.API.JSONRPC.HTTPExpr.Services = append(r.API.JSONRPC.HTTPExpr.Services, httpSvc)
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
func ModifyExampleFiles(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok || r.API == nil || r.API.JSONRPC == nil {
			continue
		}
		// Build JSON-RPC services data
		servicesData := httpcodegen.NewServicesData(service.NewServicesData(r), &r.API.JSONRPC.HTTPExpr)
		servicesData.Root = r
		// Collect MCP-enabled services (generic, no example-specific names)
		var mcpServices []*expr.ServiceExpr
		for _, sv := range r.Services {
			if mcpexpr.Root.HasMCP(sv) {
				mcpServices = append(mcpServices, sv)
			}
		}
		for _, svr := range r.API.Servers {
			svrdata := example.Servers.Get(svr, r)
			mainPath := filepath.Join("cmd", svrdata.Dir, "main.go")
			httpPath := filepath.Join("cmd", svrdata.Dir, "http.go")
			for _, f := range files {
				if f.Path == mainPath {
					for i, s := range f.SectionTemplates {
						if s.Name == "server-main-services" {
							// Append a section to wire MCP adapters generically for all MCP-enabled services
							src := "\n\t\t{\n\t\t\t// Wire MCP adapters on top of original services\n\t\t\t// Provide a simple prompt provider implementation so Prompts.get works.\n\t\t\tprovider := assistantapi.NewPromptProvider()\n"
							for _, sv := range mcpServices {
								svcSnake := codegen.SnakeCase(sv.Name)
								mcpSvcSnake := "mcp_" + svcSnake
								origVar := codegen.Goify(svcSnake, false) + "Svc"
								mcpVar := codegen.Goify(mcpSvcSnake, false) + "Svc"
								mcpAlias := "mcp" + strings.ReplaceAll(svcSnake, "_", "")
								src += "\t\t\t" + mcpVar + " = " + mcpAlias + ".NewMCPAdapter(" + origVar + ", provider, nil)\n"
							}
							src += "\t\t}\n"
							injection := &codegen.SectionTemplate{
								Name:   "server-main-services-mcp-wire",
								Source: src,
							}
							// Insert right after the services section
							n := make([]*codegen.SectionTemplate, 0, len(f.SectionTemplates)+1)
							n = append(n, f.SectionTemplates[:i+1]...)
							n = append(n, injection)
							n = append(n, f.SectionTemplates[i+1:]...)
							f.SectionTemplates = n
							break
						}
					}
					for _, s := range f.SectionTemplates {
						if s.Name != "server-http-start" {
							continue
						}
						// Collect JSON-RPC services for this server
						var jsonrpcSvcData []*httpcodegen.ServiceData
						for _, name := range svr.Services {
							if d := servicesData.Get(name); d != nil {
								jsonrpcSvcData = append(jsonrpcSvcData, d)
							}
						}
						if dataMap, ok := s.Data.(map[string]any); ok {
							dataMap["JSONRPCServices"] = jsonrpcSvcData
						}
						s.Source = serverHTTPStartJSONRPCTemplate
					}
				}
				if f.Path == httpPath {
					// Collect JSON-RPC services for this server once
					var jsonrpcSvcData []*httpcodegen.ServiceData
					for _, name := range svr.Services {
						if d := servicesData.Get(name); d != nil {
							jsonrpcSvcData = append(jsonrpcSvcData, d)
						}
					}
					for _, s := range f.SectionTemplates {
						if s.Name == "server-http-start" {
							if dataMap, ok := s.Data.(map[string]any); ok {
								dataMap["JSONRPCServices"] = jsonrpcSvcData
							}
							// Use swapped template so signature is Endpoints then Svc, matching main.go call
							s.Source = serverHTTPStartJSONRPC
						}
						if s.Name == "server-http-init" {
							// Ensure JSONRPCServices available
							if dataMap, ok := s.Data.(map[string]any); ok {
								dataMap["JSONRPCServices"] = jsonrpcSvcData
							}
						}
					}
				}
				// Rewrite JSON-RPC CLI import generically to point to MCP service client
				cliPath := filepath.Join("cmd", svrdata.Dir+"-cli", "jsonrpc.go")
				if f.Path == cliPath {
					for _, s := range f.SectionTemplates {
						if s.Source == "" {
							continue
						}
						for _, sv := range mcpServices {
							svcSnake := codegen.SnakeCase(sv.Name)
							old := "/gen/jsonrpc/cli/" + svcSnake + "\""
							newp := "/gen/jsonrpc/mcp_" + svcSnake + "/client\""
							s.Source = strings.ReplaceAll(s.Source, old, newp)
						}
					}
				}
				// Patch example assistant implementation to avoid using Stream.Close/Recv on assistant.Stream
				if f.Path == "assistant.go" {
					for _, s := range f.SectionTemplates {
						if s.Source == "" {
							continue
						}
						// Remove defer stream.Close() lines
						s.Source = strings.ReplaceAll(s.Source, "\n\tdefer stream.Close()\n", "\n")
						// Replace HandleStream body with a minimal no-op that exits on context cancel
						sig := "func (s *assistantsrvc) HandleStream(ctx context.Context, stream assistant.Stream) error {"
						idx := strings.Index(s.Source, sig)
						if idx >= 0 {
							tail := s.Source[idx+len(sig):]
							// find end of function by matching last closing brace in this section
							if end := strings.LastIndex(tail, "}"); end > 0 {
								body := "\n\tlog.Printf(ctx, \"assistant.HandleStream\")\n\t// (no-op example)\n\tselect {\n\tcase <-ctx.Done():\n\t\treturn ctx.Err()\n\tdefault:\n\t\treturn nil\n\t}\n"
								s.Source = s.Source[:idx+len(sig)] + body + tail[end:]
							}
						}
					}
				}
			}
		}
	}
	// Upstream Goa fix for mixed JSON-RPC handler avoids duplicate ServeHTTP.
	return files, nil
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

// serverHTTPStartJSONRPCTemplate mirrors Goa JSON-RPC server_http_start.go.tpl for handleHTTPServer signature
const serverHTTPStartJSONRPCTemplate = `

func handleHTTPServer(ctx context.Context, u *url.URL{{ range $.Services }}{{ if .Service.Methods }}, {{ .Service.VarName }}Endpoints *{{ .Service.PkgName }}.Endpoints{{ end }}{{ end }}{{ range $.JSONRPCServices }}, {{ .Service.VarName }}Endpoints *{{ .Service.PkgName }}.Endpoints, {{ .Service.VarName }}Svc {{ .Service.PkgName }}.Service{{ end }}, wg *sync.WaitGroup, errc chan error, dbg bool) {
`

// serverHTTPStartJSONRPC defines handleHTTPServer signature with JSON-RPC args ordered as Endpoints then Service to match main.go call
const serverHTTPStartJSONRPC = `

func handleHTTPServer(ctx context.Context, u *url.URL{{ range $.Services }}{{ if .Service.Methods }}, {{ .Service.VarName }}Endpoints *{{ .Service.PkgName }}.Endpoints{{ end }}{{ end }}{{ range $.JSONRPCServices }}, {{ .Service.VarName }}Endpoints *{{ .Service.PkgName }}.Endpoints, {{ .Service.VarName }}Svc {{ .Service.PkgName }}.Service{{ end }}, wg *sync.WaitGroup, errc chan error, dbg bool) {
`
