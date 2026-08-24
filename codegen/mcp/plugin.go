// This file adds MCP services before Goa chooses Go names, then adds files that
// register MCP methods, call the user service, and pass policy headers to MCP
// handlers.
package codegen

import (
	"fmt"
	"path"
	"slices"
	"strings"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goacodegen "goa.design/goa/v3/codegen"
	goagenerator "goa.design/goa/v3/codegen/generator"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// preparedMCPService stores the design root, user service, attached MCP
	// service, prompts, and method map for one user service.
	preparedMCPService struct {
		root        *expr.RootExpr
		userService *expr.ServiceExpr
		mcpService  *expr.ServiceExpr
		mcp         *mcpexpr.MCPExpr
		prompts     []*mcpexpr.DynamicPromptExpr
		mapping     *ServiceMethodMapping
	}

	// plannedMCPService stores the attached service, Goa's saved service types,
	// and the names and types used to write its MCP files.
	plannedMCPService struct {
		prepared     *preparedMCPService
		servicePlan  *goaservice.Plan
		adapterData  *AdapterData
		codecPlan    *jsoncodec.Plan
		methodCodecs map[string]*plannedMethodCodec
	}

	// plannedMethodCodec stores the generated JSON value for each side of one
	// original service method.
	plannedMethodCodec struct {
		payload *jsoncodec.Value
		result  *jsoncodec.Value
	}

	// mcpPlugin stores the MCP services added during Prepare and the file data
	// saved during Plan for one command.
	mcpPlugin struct {
		prepared []*preparedMCPService
		planned  []*plannedMCPService
	}
)

// newMCPPlugin returns a plugin whose Prepare, Plan, and Generate methods share
// a new mcpPlugin for one command.
func newMCPPlugin() goagenerator.Plugin {
	plugin := new(mcpPlugin)
	return goagenerator.Plugin{
		Prepare:  plugin.prepare,
		Plan:     plugin.plan,
		Generate: plugin.generate,
	}
}

// prepare adds the generated MCP services before Goa chooses Go names and files.
func (p *mcpPlugin) prepare(_ string, roots []eval.Root) error {
	prepared, err := prepareMCPServices(roots)
	if err != nil {
		return err
	}
	p.prepared = prepared
	return nil
}

// plan saves Goa's service plan and reserves the Go names written by MCP files.
func (p *mcpPlugin) plan(plan *goagenerator.Plan) error {
	for _, prepared := range p.prepared {
		servicePlan := plan.Service(prepared.root)
		adapter, err := newAdapterGenerator(
			prepared.userService,
			prepared.mcp,
			prepared.prompts,
			prepared.mapping,
		).buildAdapterData()
		if err != nil {
			return err
		}
		if err := bindResourceQueryClientSelectors(servicePlan, prepared.mcp.Resources, adapter.Resources); err != nil {
			return err
		}
		if err := planMCPPackagePaths(servicePlan, prepared, adapter); err != nil {
			return err
		}
		if err := declareMCPNames(plan.Generation(), adapter); err != nil {
			return err
		}
		codecPlan, methodCodecs, err := planMCPCodecs(plan.Generation(), prepared, adapter)
		if err != nil {
			return err
		}
		if err := planMCPImports(plan.Generation(), prepared, adapter); err != nil {
			return err
		}
		p.planned = append(p.planned, &plannedMCPService{
			prepared:     prepared,
			servicePlan:  servicePlan,
			adapterData:  adapter,
			codecPlan:    codecPlan,
			methodCodecs: methodCodecs,
		})
	}
	return nil
}

// generate adds files that register MCP methods and call the user service. It
// also updates the JSON-RPC server so allow and deny headers reach MCP handlers.
func (p *mcpPlugin) generate(plan *goagenerator.Plan, files []*goacodegen.File) ([]*goacodegen.File, error) {
	for _, planned := range p.planned {
		services := planned.servicePlan.Services()
		mcpService := services.Get(planned.prepared.mcpService.Name)
		if mcpService == nil {
			return nil, fmt.Errorf("Goa did not plan MCP service %q", planned.prepared.mcpService.Name)
		}
		userService := services.Get(planned.prepared.userService.Name)
		if userService == nil {
			return nil, fmt.Errorf("Goa did not plan original service %q", planned.prepared.userService.Name)
		}
		if err := bindUserServiceMethods(services, userService, planned); err != nil {
			return nil, err
		}
		if err := bindMCPServiceMethods(planned.adapterData, mcpService); err != nil {
			return nil, err
		}
		if err := bindMCPJSONRPCMethods(plan, planned); err != nil {
			return nil, err
		}
		bindMCPImports(planned.adapterData)
		planned.adapterData.MCPPackage = mcpService.PkgName
		codecFiles, err := bindMCPCodecs(services, planned)
		if err != nil {
			return nil, err
		}
		files = append(files, codecFiles...)
		if register := registerFile(planned.adapterData); register != nil {
			files = append(files, register)
		}
		if caller := clientCallerFile(planned.adapterData); caller != nil {
			files = append(files, caller)
		}
		files = append(files, generateMCPTransport(
			services.GenPkg(),
			planned.prepared.userService,
			planned.adapterData,
		)...)
		files = append(files, generateMCPClientAdapter(
			services.GenPkg(),
			planned.prepared.userService,
			planned.adapterData,
		)...)
	}
	applyMCPPolicyHeadersToJSONRPCMount(files, p.planned)
	return files, nil
}

// planMCPPackagePaths copies the exact generated user and MCP package paths
// chosen by Goa before plugin files claim or import those packages.
func planMCPPackagePaths(
	servicePlan *goaservice.Plan,
	prepared *preparedMCPService,
	data *AdapterData,
) error {
	serviceImport, _, err := servicePlan.ServicePackageImports(prepared.userService)
	if err != nil {
		return fmt.Errorf("plan MCP user service package: %w", err)
	}
	mcpImport, _, err := servicePlan.ServicePackageImports(prepared.mcpService)
	if err != nil {
		return fmt.Errorf("plan MCP service package: %w", err)
	}
	data.serviceGeneratedImport = serviceImport
	data.mcpGeneratedImport = mcpImport
	data.serviceImportPath = serviceImport.Path
	data.mcpImportPath = mcpImport.Path
	data.mcpPathName = path.Base(mcpImport.Path)
	return nil
}

// bindMCPJSONRPCMethods copies the exact request and response helper names used
// by the generated JSON-RPC client.
func bindMCPJSONRPCMethods(plan *goagenerator.Plan, planned *plannedMCPService) error {
	jsonrpcPlan, ok := plan.JSONRPC(planned.prepared.root)
	if !ok {
		return fmt.Errorf("Goa did not plan JSON-RPC for MCP service %q", planned.prepared.mcpService.Name)
	}
	var transport *expr.HTTPServiceExpr
	for _, service := range planned.prepared.root.API.JSONRPC.Services {
		if service.ServiceExpr == planned.prepared.mcpService {
			transport = service
			break
		}
	}
	if transport == nil {
		return fmt.Errorf("Goa did not plan JSON-RPC transport for MCP service %q", planned.prepared.mcpService.Name)
	}
	service, ok := jsonrpcPlan.Service(transport)
	if !ok {
		return fmt.Errorf("Goa did not link JSON-RPC service %q", planned.prepared.mcpService.Name)
	}
	for _, notification := range planned.adapterData.Notifications {
		var found bool
		for _, endpoint := range service.Endpoints {
			if endpoint.Method.Name != notification.WireMethodName {
				continue
			}
			notification.RequestBuilderName = endpoint.RequestInit.Declaration.Name()
			notification.ResponseDecoderName = endpoint.ResponseDecoderDeclaration.Name()
			found = true
			break
		}
		if !found {
			return fmt.Errorf("Goa did not plan JSON-RPC notification method %q", notification.WireMethodName)
		}
	}
	return nil
}

// planMCPImports submits every package name used by MCP files before Goa
// chooses import names for their output packages.
func planMCPImports(
	generation *goacodegen.Generation,
	prepared *preparedMCPService,
	data *AdapterData,
) error {
	data.jsonrpcClientImportPath = path.Join(generation.GenPkg(), "jsonrpc", data.mcpPathName, "client")
	data.mcpPackage = generation.Package(data.mcpImportPath)

	serverFixed := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"),
		goacodegen.SimpleImport("encoding/json"),
		goacodegen.SimpleImport("sync"),
		goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
		goacodegen.NewImport("goahttp", "goa.design/goa/v3/http"),
		goacodegen.NewImport("goa", "goa.design/goa/v3/pkg"),
	}
	data.serverImportPaths = []string{
		"context",
		"encoding/json",
		"sync",
		data.serviceImportPath,
		"goa.design/goa-ai/runtime/mcp",
		"goa.design/goa/v3/http",
		"goa.design/goa/v3/pkg",
	}
	if len(data.Resources) > 0 || data.NeedsNoArgumentsValidation {
		serverFixed = append(serverFixed, goacodegen.SimpleImport("fmt"))
		data.serverImportPaths = append(data.serverImportPaths, "fmt")
	}
	if len(data.Resources) > 0 {
		serverFixed = append(serverFixed, goacodegen.SimpleImport("net/url"), goacodegen.SimpleImport("strings"))
		data.serverImportPaths = append(data.serverImportPaths, "net/url", "strings")
	}
	if data.NeedsQueryFormatting {
		serverFixed = append(serverFixed, goacodegen.SimpleImport("strconv"))
		data.serverImportPaths = append(data.serverImportPaths, "strconv")
	}
	if err := requireImports(data.mcpPackage, serverFixed); err != nil {
		return fmt.Errorf("plan MCP server imports: %w", err)
	}
	if err := data.mcpPackage.ReserveGeneratedImport(data.serviceGeneratedImport); err != nil {
		return fmt.Errorf("plan MCP server service import: %w", err)
	}
	if data.NeedsServerCodec {
		if err := data.mcpPackage.ReserveGeneratedImport(goacodegen.NewImport("mcpcodec", data.CodecImportPath)); err != nil {
			return fmt.Errorf("plan MCP server codec import: %w", err)
		}
		data.serverImportPaths = append(data.serverImportPaths, data.CodecImportPath)
	}

	if len(data.StaticPrompts) > 0 || len(data.DynamicPrompts) > 0 {
		provider := []*goacodegen.ImportSpec{goacodegen.SimpleImport("encoding/json")}
		data.promptProviderImportPaths = []string{"encoding/json"}
		if len(data.DynamicPrompts) > 0 {
			provider = append(provider, goacodegen.SimpleImport("context"))
			data.promptProviderImportPaths = append(data.promptProviderImportPaths, "context")
		}
		if err := requireImports(data.mcpPackage, provider); err != nil {
			return fmt.Errorf("plan MCP prompt provider imports: %w", err)
		}
	}
	if data.Register != nil {
		register := []*goacodegen.ImportSpec{
			goacodegen.SimpleImport("context"),
			goacodegen.SimpleImport("encoding/json"),
			goacodegen.SimpleImport("errors"),
			goacodegen.SimpleImport("strings"),
			goacodegen.NewImport("planner", "goa.design/goa-ai/runtime/agent/planner"),
			goacodegen.NewImport("policy", "goa.design/goa-ai/runtime/agent/policy"),
			goacodegen.NewImport("rawjson", "goa.design/goa-ai/runtime/agent/rawjson"),
			goacodegen.NewImport("agentsruntime", "goa.design/goa-ai/runtime/agent/runtime"),
			goacodegen.NewImport("telemetry", "goa.design/goa-ai/runtime/agent/telemetry"),
			goacodegen.NewImport("tools", "goa.design/goa-ai/runtime/agent/tools"),
			goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
		}
		for _, spec := range register {
			data.registerImportPaths = append(data.registerImportPaths, spec.Path)
		}
		if err := requireImports(data.mcpPackage, register); err != nil {
			return fmt.Errorf("plan MCP registry imports: %w", err)
		}
	}
	if err := planMCPClientImports(generation, prepared, data); err != nil {
		return err
	}
	return planMCPCallerImports(data)
}

// planMCPClientImports submits the imports used by the adapter client and its
// exact enabled feature set.
func planMCPClientImports(
	generation *goacodegen.Generation,
	prepared *preparedMCPService,
	data *AdapterData,
) error {
	if err := planMCPClientPayloadImports(generation, prepared, data); err != nil {
		return err
	}
	clientPackage := generation.Package(data.clientPackagePath)
	fixed := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("net/http"),
		goacodegen.NewImport("goahttp", "goa.design/goa/v3/http"),
	}
	data.clientImportPaths = []string{"net/http", "goa.design/goa/v3/http", data.serviceImportPath}
	if data.NeedsMCPClient {
		fixed = append(fixed, goacodegen.SimpleImport("context"))
		data.clientImportPaths = append(data.clientImportPaths, "context", data.jsonrpcClientImportPath)
	}
	if len(data.Tools) > 0 || len(data.Resources) > 0 || len(data.DynamicPrompts) > 0 {
		data.clientImportPaths = append(data.clientImportPaths, data.mcpImportPath)
	}
	if len(data.Tools) > 0 || len(data.Resources) > 0 || len(data.DynamicPrompts) > 0 {
		fixed = append(fixed, goacodegen.SimpleImport("fmt"))
		data.clientImportPaths = append(data.clientImportPaths, "fmt")
	}
	for _, resource := range data.Resources {
		if resource.HasPayload {
			fixed = append(fixed, goacodegen.SimpleImport("net/url"))
			data.clientImportPaths = append(data.clientImportPaths, "net/url")
			break
		}
	}
	if data.NeedsQueryFormatting {
		fixed = append(fixed, goacodegen.SimpleImport("strconv"))
		data.clientImportPaths = append(data.clientImportPaths, "strconv")
	}
	if len(data.Notifications) > 0 {
		fixed = append(fixed,
			goacodegen.SimpleImport("encoding/json"),
			goacodegen.NewImport("jsonrpc", "goa.design/goa/v3/jsonrpc"),
			goacodegen.NewImport("uuid", "github.com/google/uuid"),
		)
		data.clientImportPaths = append(data.clientImportPaths,
			"encoding/json",
			"goa.design/goa/v3/jsonrpc",
			"github.com/google/uuid",
		)
	}
	if err := requireImports(clientPackage, fixed); err != nil {
		return fmt.Errorf("plan MCP client fixed imports: %w", err)
	}
	if data.NeedsMCPClient {
		preferred := goacodegen.Goify(data.mcpPathName, false) + "jsonrpcc"
		if err := clientPackage.ReserveGeneratedImport(goacodegen.NewImport(preferred, data.jsonrpcClientImportPath)); err != nil {
			return fmt.Errorf("plan MCP JSON-RPC client import: %w", err)
		}
	}
	if len(data.Tools) > 0 || len(data.Resources) > 0 || len(data.DynamicPrompts) > 0 {
		if err := clientPackage.ReserveGeneratedImport(data.mcpGeneratedImport); err != nil {
			return fmt.Errorf("plan MCP client service import: %w", err)
		}
	}
	if data.NeedsClientCodec {
		if err := clientPackage.ReserveGeneratedImport(goacodegen.NewImport("mcpcodec", data.CodecImportPath)); err != nil {
			return fmt.Errorf("plan MCP client codec import: %w", err)
		}
		data.clientImportPaths = append(data.clientImportPaths, data.CodecImportPath)
	}
	for importPath := range data.clientPayloadImportPaths {
		data.clientImportPaths = append(data.clientImportPaths, importPath)
	}
	return nil
}

// planMCPCallerImports submits the imports used by the optional runtime caller.
func planMCPCallerImports(data *AdapterData) error {
	if data.ClientCaller == nil {
		return nil
	}
	pkg := data.ClientCaller.clientPackage
	fixed := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"),
		goacodegen.SimpleImport("encoding/json"),
		goacodegen.SimpleImport("errors"),
		goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
	}
	for _, spec := range fixed {
		data.ClientCaller.clientImportPaths = append(data.ClientCaller.clientImportPaths, spec.Path)
	}
	data.ClientCaller.clientImportPaths = append(data.ClientCaller.clientImportPaths, data.mcpImportPath)
	data.ClientCaller.clientImportPaths = append(data.ClientCaller.clientImportPaths, "goa.design/goa/v3/jsonrpc")
	if err := requireImports(pkg, fixed); err != nil {
		return fmt.Errorf("plan MCP caller fixed imports: %w", err)
	}
	if err := pkg.DeclareImport(goacodegen.NewImport("genjsonrpc", "goa.design/goa/v3/jsonrpc")); err != nil {
		return fmt.Errorf("plan MCP caller JSON-RPC import: %w", err)
	}
	if err := pkg.ReserveGeneratedImport(data.mcpGeneratedImport); err != nil {
		return fmt.Errorf("plan MCP caller service import: %w", err)
	}
	return nil
}

// bindMCPImports reads the exact import names Goa chose for every MCP output.
func bindMCPImports(data *AdapterData) {
	data.serverImports = packageImports(data.mcpPackage, data.serverImportPaths)
	data.promptProviderImports = packageImports(data.mcpPackage, data.promptProviderImportPaths)
	data.registerImports = packageImports(data.mcpPackage, data.registerImportPaths)
	data.Package = data.mcpPackage.ImportName(data.serviceImportPath)
	if data.NeedsServerCodec {
		data.CodecPackage = data.mcpPackage.ImportName(data.CodecImportPath)
	}
	client := data.clientPackagePath
	if client != "" {
		pkg := data.clientPackage
		data.clientImports = packageImports(pkg, data.clientImportPaths)
		data.clientPackageName = path.Base(data.clientPackagePath)
		data.clientServicePackage = pkg.ImportName(data.serviceImportPath)
		if data.NeedsMCPClient {
			data.clientJSONRPCPackage = pkg.ImportName(data.jsonrpcClientImportPath)
		}
		if len(data.Tools) > 0 || len(data.Resources) > 0 || len(data.DynamicPrompts) > 0 {
			data.clientMCPPackage = pkg.ImportName(data.mcpImportPath)
		}
		if data.NeedsClientCodec {
			data.clientCodecPackage = pkg.ImportName(data.CodecImportPath)
		}
	}
	if data.ClientCaller != nil {
		data.ClientCaller.imports = packageImports(data.ClientCaller.clientPackage, data.ClientCaller.clientImportPaths)
		data.ClientCaller.MCPPackage = data.ClientCaller.clientPackage.ImportName(data.mcpImportPath)
		data.ClientCaller.JSONRPCPackage = data.ClientCaller.clientPackage.ImportName("goa.design/goa/v3/jsonrpc")
	}
}

// requireImports submits imports whose qualifiers are written literally in templates.
func requireImports(pkg *goacodegen.GeneratedPackage, imports []*goacodegen.ImportSpec) error {
	for _, spec := range imports {
		if err := pkg.RequireImport(spec); err != nil {
			return err
		}
	}
	return nil
}

// packageImports returns the import lines Goa chose for one generated file.
func packageImports(pkg *goacodegen.GeneratedPackage, paths []string) []*goacodegen.ImportSpec {
	seen := make(map[string]struct{}, len(paths))
	imports := make([]*goacodegen.ImportSpec, 0, len(paths))
	for _, importPath := range paths {
		if _, ok := seen[importPath]; ok {
			continue
		}
		seen[importPath] = struct{}{}
		imports = append(imports, pkg.Import(importPath))
	}
	return imports
}

// bindMCPServiceMethods copies the final Goa method and payload names used by
// generated adapter methods.
func bindMCPServiceMethods(data *AdapterData, service *goaservice.Data) error {
	for _, notification := range data.Notifications {
		method := service.Method(notification.WireMethodName)
		if method == nil {
			return fmt.Errorf("Goa did not plan MCP notification method %q", notification.WireMethodName)
		}
		notification.MCPMethodName = method.VarName
		notification.PayloadRef = method.PayloadRef
	}
	return nil
}

// planMCPClientPayloadImports records every package that a client payload type
// can name before Goa chooses the final import names.
func planMCPClientPayloadImports(
	generation *goacodegen.Generation,
	prepared *preparedMCPService,
	data *AdapterData,
) error {
	clientPath := path.Join(data.mcpImportPath, "adapter/client")
	clientPackage := generation.Package(clientPath)
	data.clientPackage = clientPackage
	if err := clientPackage.ReserveGeneratedImport(data.serviceGeneratedImport); err != nil {
		return fmt.Errorf("plan MCP client service import: %w", err)
	}
	data.clientPackagePath = clientPath
	data.clientPayloadImportPaths = make(map[string]struct{})
	for _, method := range mappedMCPMethods(prepared) {
		if !hasMCPValue(method.Payload) {
			continue
		}
		if location := goacodegen.UserTypeLocation(method.Payload.Type); location != nil {
			importPath := path.Join(generation.GenPkg(), location.RelImportPath)
			preference := strings.ToLower(goacodegen.Goify(path.Base(importPath), false))
			if err := clientPackage.ReserveGeneratedImport(goacodegen.NewImport(preference, importPath)); err != nil {
				return fmt.Errorf("plan MCP client payload import for method %q: %w", method.Name, err)
			}
			data.clientPayloadImportPaths[importPath] = struct{}{}
		}
		if _, spec := goacodegen.GetMetaType(method.Payload); spec != nil {
			if err := clientPackage.DeclareImport(goacodegen.NewImport(spec.Name, spec.Path)); err != nil {
				return fmt.Errorf("plan MCP client payload import for method %q: %w", method.Name, err)
			}
			data.clientPayloadImportPaths[spec.Path] = struct{}{}
		}
	}
	return nil
}

// bindUserServiceMethods copies each original method's final selector and
// payload type after Goa has chosen package and declaration names.
func bindUserServiceMethods(
	services *goaservice.ServicesData,
	service *goaservice.Data,
	planned *plannedMCPService,
) error {
	data := planned.adapterData
	attributor := services.ServiceAttributor(service.Name, data.clientPackagePath)
	data.clientMethodNames = make([]string, len(service.Methods))
	for index, method := range service.Methods {
		data.clientMethodNames[index] = method.VarName
	}
	var err error
	for _, tool := range data.Tools {
		var method *goaservice.MethodData
		method, tool.PayloadType, err = bindUserServiceMethod(
			attributor,
			service,
			planned.prepared.userService,
			tool.userMethodName,
			tool.HasPayload,
		)
		if err != nil {
			return err
		}
		tool.ServiceMethodName = method.VarName
	}
	for _, resource := range data.Resources {
		var method *goaservice.MethodData
		method, resource.PayloadType, err = bindUserServiceMethod(
			attributor,
			service,
			planned.prepared.userService,
			resource.userMethodName,
			resource.HasPayload,
		)
		if err != nil {
			return err
		}
		resource.ServiceMethodName = method.VarName
	}
	for _, prompt := range data.DynamicPrompts {
		var method *goaservice.MethodData
		method, prompt.PayloadType, err = bindUserServiceMethod(
			attributor,
			service,
			planned.prepared.userService,
			prompt.userMethodName,
			prompt.HasPayload,
		)
		if err != nil {
			return err
		}
		prompt.ServiceMethodName = method.VarName
	}
	for _, notification := range data.Notifications {
		var method *goaservice.MethodData
		method, notification.PayloadType, err = bindUserServiceMethod(
			attributor,
			service,
			planned.prepared.userService,
			notification.userMethodName,
			true,
		)
		if err != nil {
			return err
		}
		notification.ServiceMethodName = method.VarName
	}
	if data.Register != nil {
		for index, tool := range data.Tools {
			payloadType := tool.PayloadType
			if payloadType == "" {
				payloadType = "any"
			}
			data.Register.Tools[index].PayloadType = payloadType
		}
	}
	return nil
}

// bindUserServiceMethod returns Goa's final method record and the payload type
// as named from the generated client adapter package.
func bindUserServiceMethod(
	attributor goacodegen.Attributor,
	service *goaservice.Data,
	serviceExpr *expr.ServiceExpr,
	methodName string,
	hasPayload bool,
) (*goaservice.MethodData, string, error) {
	method := service.Method(methodName)
	if method == nil {
		return nil, "", fmt.Errorf("Goa did not plan original service method %q", methodName)
	}
	if !hasPayload {
		return method, "", nil
	}
	if method.PayloadRef == "" {
		return nil, "", fmt.Errorf("Goa method %q has no planned payload type", methodName)
	}
	return method, attributor.Ref(serviceExpr.Method(method.Name).Payload, ""), nil
}

// planMCPCodecs records the private JSON package and every mapped service value
// before Goa chooses final Go names.
func planMCPCodecs(
	generation *goacodegen.Generation,
	prepared *preparedMCPService,
	data *AdapterData,
) (*jsoncodec.Plan, map[string]*plannedMethodCodec, error) {
	methods := mappedMCPMethods(prepared)
	hasValues := false
	for _, method := range methods {
		if hasMCPValue(method.Payload) || hasMCPValue(method.Result) {
			hasValues = true
			break
		}
	}
	if !hasValues {
		return nil, nil, nil
	}

	codecImportPath := path.Join(data.mcpImportPath, "internal", "codec")
	serviceImportPath := data.serviceImportPath
	planned, err := jsoncodec.NewPlan(generation, codecImportPath, "codec", serviceImportPath)
	if err != nil {
		return nil, nil, fmt.Errorf("plan MCP codecs for service %q: %w", prepared.userService.Name, err)
	}
	methodCodecs := make(map[string]*plannedMethodCodec, len(methods))
	resourceMethods := make(map[string]struct{}, len(prepared.mcp.Resources))
	for _, resource := range prepared.mcp.Resources {
		resourceMethods[resource.Method.Name] = struct{}{}
	}
	for _, method := range methods {
		values := new(plannedMethodCodec)
		preferred := goacodegen.Goify(method.Name, true)
		payloadDirection, resultDirection := mcpCodecDirections(prepared, method.Name)
		_, resourceMethod := resourceMethods[method.Name]
		if resourceMethod && payloadDirection == 0 {
			payloadDirection = jsoncodec.ConstructOnly
		}
		data.NeedsServerCodec = data.NeedsServerCodec || resourceMethod ||
			codecDirectionDecodes(payloadDirection) || codecDirectionEncodes(resultDirection)
		data.NeedsClientCodec = data.NeedsClientCodec || codecDirectionEncodes(payloadDirection) || codecDirectionDecodes(resultDirection)
		if hasMCPValue(method.Payload) && payloadDirection != 0 {
			values.payload, err = planned.Add(
				prepared.userService.Name+":"+method.Name+":payload",
				preferred+"Payload",
				method.Payload,
				payloadDirection,
			)
			if err != nil {
				return nil, nil, fmt.Errorf("plan MCP payload codec for method %q: %w", method.Name, err)
			}
			if resourceMethod {
				if err := values.payload.PlanTransportConstructor(); err != nil {
					return nil, nil, fmt.Errorf("plan MCP resource payload constructor for method %q: %w", method.Name, err)
				}
			}
		}
		if hasMCPValue(method.Result) && resultDirection != 0 {
			values.result, err = planned.Add(
				prepared.userService.Name+":"+method.Name+":result",
				preferred+"Result",
				method.Result,
				resultDirection,
			)
			if err != nil {
				return nil, nil, fmt.Errorf("plan MCP result codec for method %q: %w", method.Name, err)
			}
		}
		methodCodecs[method.Name] = values
	}
	data.CodecImportPath = codecImportPath
	data.CodecPackage = "mcpcodec"
	return planned, methodCodecs, nil
}

// codecDirectionEncodes reports whether a planned value writes JSON.
func codecDirectionEncodes(direction jsoncodec.Direction) bool {
	return direction == jsoncodec.EncodeOnly || direction == jsoncodec.EncodeAndDecode
}

// codecDirectionDecodes reports whether a planned value reads JSON.
func codecDirectionDecodes(direction jsoncodec.Direction) bool {
	return direction == jsoncodec.DecodeOnly || direction == jsoncodec.EncodeAndDecode
}

// bindMCPCodecs joins codec types to Goa's final service declarations and adds
// the chosen function names to the server and client template data.
func bindMCPCodecs(services *goaservice.ServicesData, planned *plannedMCPService) ([]*goacodegen.File, error) {
	if planned.codecPlan == nil {
		return nil, nil
	}
	attributor := services.ServiceAttributor(
		planned.prepared.userService.Name,
		planned.adapterData.CodecImportPath,
	)
	for _, method := range mappedMCPMethods(planned.prepared) {
		values := planned.methodCodecs[method.Name]
		for _, value := range []*jsoncodec.Value{values.payload, values.result} {
			if value == nil {
				continue
			}
			if err := value.BindService(attributor); err != nil {
				return nil, fmt.Errorf("bind MCP codec for method %q: %w", method.Name, err)
			}
		}
	}
	bindMCPCodecData(planned.adapterData, planned.methodCodecs)
	if err := bindResourceQueryTransportFields(planned); err != nil {
		return nil, err
	}
	files, err := planned.codecPlan.Files()
	if err != nil {
		return nil, fmt.Errorf("render MCP codecs for service %q: %w", planned.prepared.userService.Name, err)
	}
	return files, nil
}

// bindResourceQueryTransportFields copies the private codec field and type
// names into each server resource adapter after Goa has fixed every name.
func bindResourceQueryTransportFields(planned *plannedMCPService) error {
	data := planned.adapterData
	qualifier := func(importPath string) string {
		if importPath == data.CodecImportPath {
			return data.CodecPackage
		}
		return data.mcpPackage.ImportName(importPath)
	}
	for index, resource := range planned.prepared.mcp.Resources {
		if resource.Method.Payload == nil || resource.Method.Payload.Type == expr.Empty {
			continue
		}
		value := planned.methodCodecs[resource.Method.Name].payload
		typeName, err := value.TransportTypeName(data.mcpImportPath, qualifier)
		if err != nil {
			return fmt.Errorf("bind resource method %q transport type: %w", resource.Method.Name, err)
		}
		adapter := data.Resources[index]
		adapter.Codec.PayloadTransport = typeName
		for _, queryField := range adapter.QueryFields {
			field, err := value.TransportField(
				queryField.attribute,
				queryField.QueryKey,
				data.mcpImportPath,
				qualifier,
			)
			if err != nil {
				return fmt.Errorf(
					"bind resource method %q transport field %q: %w",
					resource.Method.Name,
					queryField.QueryKey,
					err,
				)
			}
			queryField.TransportSelector = field.Selector
			queryField.TransportType = field.TypeRef
			queryField.TransportValueType = field.ValueTypeRef
			queryField.TransportPointer = field.Pointer
			queryField.TransportElementType = field.ElementTypeRef
			queryField.TransportElementPointer = field.ElementPointer
		}
	}
	return nil
}

// bindMCPCodecData copies final generated function names to each mapped MCP
// method and records which generated adapters import the private codec package.
func bindMCPCodecData(data *AdapterData, methods map[string]*plannedMethodCodec) {
	for _, tool := range data.Tools {
		tool.Codec = methodCodecData(methods[tool.userMethodName])
		data.addCodecImports(tool.Codec)
	}
	for _, resource := range data.Resources {
		resource.Codec = methodCodecData(methods[resource.userMethodName])
		data.addCodecImports(resource.Codec)
	}
	for _, prompt := range data.DynamicPrompts {
		prompt.Codec = methodCodecData(methods[prompt.userMethodName])
		data.addCodecImports(prompt.Codec)
	}
	for _, notification := range data.Notifications {
		notification.Codec = methodCodecData(methods[notification.userMethodName])
		data.addCodecImports(notification.Codec)
	}
}

// addCodecImports records which generated adapter calls the private JSON
// package.
func (data *AdapterData) addCodecImports(codec *MethodCodecData) {
	if codec == nil {
		return
	}
	data.NeedsServerCodec = data.NeedsServerCodec || codec.PayloadDecode != "" ||
		codec.PayloadNew != "" || codec.ResultEncode != ""
	data.NeedsClientCodec = data.NeedsClientCodec || codec.PayloadEncode != "" || codec.ResultDecode != ""
}

// methodCodecData returns the final generated names for one service method.
func methodCodecData(planned *plannedMethodCodec) *MethodCodecData {
	if planned == nil || planned.payload == nil && planned.result == nil {
		return nil
	}
	data := new(MethodCodecData)
	if planned.payload != nil {
		if declaration := planned.payload.EncodeDeclaration(); declaration != nil {
			data.PayloadEncode = declaration.Name()
		}
		if declaration := planned.payload.DecodeDeclaration(); declaration != nil {
			data.PayloadDecode = declaration.Name()
		}
		if declaration := planned.payload.TransportConstructorDeclaration(); declaration != nil {
			data.PayloadNew = declaration.Name()
		}
	}
	if planned.result != nil {
		if declaration := planned.result.EncodeDeclaration(); declaration != nil {
			data.ResultEncode = declaration.Name()
		}
		if declaration := planned.result.DecodeDeclaration(); declaration != nil {
			data.ResultDecode = declaration.Name()
		}
	}
	return data
}

// mcpCodecDirections returns the conversions used by every MCP feature mapped
// to methodName.
func mcpCodecDirections(prepared *preparedMCPService, methodName string) (jsoncodec.Direction, jsoncodec.Direction) {
	var payloadEncode, payloadDecode, resultEncode, resultDecode bool
	for _, tool := range prepared.mcp.Tools {
		if tool.Method.Name == methodName {
			payloadEncode, payloadDecode = true, true
			resultEncode, resultDecode = true, true
		}
	}
	for _, resource := range prepared.mcp.Resources {
		if resource.Method.Name == methodName {
			resultEncode, resultDecode = true, true
		}
	}
	for _, prompt := range prepared.prompts {
		if prompt.Method.Name == methodName {
			payloadEncode, payloadDecode = true, true
			resultDecode = true
		}
	}
	for _, notification := range prepared.mcp.Notifications {
		if notification.Method.Name == methodName {
			payloadEncode = true
		}
	}
	return codecDirection(payloadEncode, payloadDecode), codecDirection(resultEncode, resultDecode)
}

// codecDirection converts the generation-time use flags to the codec plan's
// direction.
func codecDirection(encode, decode bool) jsoncodec.Direction {
	switch {
	case encode && decode:
		return jsoncodec.EncodeAndDecode
	case encode:
		return jsoncodec.EncodeOnly
	case decode:
		return jsoncodec.DecodeOnly
	default:
		return 0
	}
}

// mappedMCPMethods returns each original service method used by MCP once in
// design order.
func mappedMCPMethods(prepared *preparedMCPService) []*expr.MethodExpr {
	seen := make(map[string]struct{})
	var methods []*expr.MethodExpr
	add := func(method *expr.MethodExpr) {
		if method == nil {
			return
		}
		if _, ok := seen[method.Name]; ok {
			return
		}
		seen[method.Name] = struct{}{}
		methods = append(methods, method)
	}
	for _, tool := range prepared.mcp.Tools {
		add(tool.Method)
	}
	for _, resource := range prepared.mcp.Resources {
		add(resource.Method)
	}
	for _, prompt := range prepared.prompts {
		add(prompt.Method)
	}
	for _, notification := range prepared.mcp.Notifications {
		add(notification.Method)
	}
	return methods
}

// declareMCPNames reserves each Go name written by MCP files.
func declareMCPNames(generation *goacodegen.Generation, data *AdapterData) error {
	mcpPackage, err := generation.ClaimPackage(data.mcpImportPath)
	if err != nil {
		return err
	}
	for _, declaration := range []*goacodegen.NameDeclaration{
		goacodegen.NewExactName(goacodegen.NameType, "MCPAdapter"),
		goacodegen.NewExactName(goacodegen.NameType, "MCPAdapterOptions"),
		goacodegen.NewExactName(goacodegen.NameFunction, "NewMCPAdapter"),
		goacodegen.NewExactName(goacodegen.NameFunction, "stringPtr"),
		goacodegen.NewExactName(goacodegen.NameFunction, "isLikelyJSON"),
		goacodegen.NewExactName(goacodegen.NameFunction, "buildContentItem"),
		goacodegen.NewExactName(goacodegen.NameConstant, "DefaultProtocolVersion"),
	} {
		if err := mcpPackage.DeclareName(declaration); err != nil {
			return err
		}
	}
	if data.NeedsNoArgumentsValidation {
		if err := mcpPackage.DeclareName(goacodegen.NewExactName(
			goacodegen.NameFunction,
			"validateNoArguments",
		)); err != nil {
			return err
		}
	}
	if len(data.StaticPrompts) > 0 || len(data.DynamicPrompts) > 0 {
		if err := mcpPackage.DeclareName(goacodegen.NewExactName(goacodegen.NameType, "PromptProvider")); err != nil {
			return err
		}
	}
	if data.Register != nil {
		helper := data.Register.HelperName
		for _, declaration := range []*goacodegen.NameDeclaration{
			goacodegen.NewExactName(goacodegen.NameVariable, helper+"ToolSpecs"),
			goacodegen.NewExactName(goacodegen.NameVariable, helper+"ToolMetadata"),
			goacodegen.NewExactName(goacodegen.NameFunction, helper+"ToolMetadataByName"),
			goacodegen.NewExactName(goacodegen.NameFunction, "Register"+helper),
			goacodegen.NewExactName(goacodegen.NameFunction, helper+"HandleError"),
			goacodegen.NewExactName(goacodegen.NameFunction, helper+"CorrectionFailure"),
		} {
			if err := mcpPackage.DeclareName(declaration); err != nil {
				return err
			}
		}
	}
	if err := declareMCPClientNames(generation, data); err != nil {
		return err
	}
	serverPackage, err := generation.ClaimPackage(path.Join(
		generation.GenPkg(),
		"jsonrpc/"+data.mcpPathName+"/server",
	))
	if err != nil {
		return err
	}
	return serverPackage.DeclareName(goacodegen.NewExactName(
		goacodegen.NameFunction,
		"withMCPPolicyHeaders",
	))
}

// declareMCPClientNames reserves the Go names written by MCP client files.
func declareMCPClientNames(generation *goacodegen.Generation, data *AdapterData) error {
	if data.ClientCaller != nil {
		clientPackage, err := generation.ClaimPackage(path.Join(
			generation.GenPkg(),
			"jsonrpc/"+data.mcpPathName+"/client",
		))
		if err != nil {
			return err
		}
		data.ClientCaller.clientPackage = clientPackage
		for _, declaration := range []*goacodegen.NameDeclaration{
			goacodegen.NewExactName(goacodegen.NameType, "Caller"),
			goacodegen.NewExactName(goacodegen.NameFunction, "NewCaller"),
			goacodegen.NewExactName(goacodegen.NameFunction, "callerError"),
		} {
			if err := clientPackage.DeclareName(declaration); err != nil {
				return err
			}
		}
	}
	adapterPackage, err := generation.ClaimPackage(path.Join(
		data.mcpImportPath,
		"adapter/client",
	))
	if err != nil {
		return err
	}
	names := []*goacodegen.NameDeclaration{
		goacodegen.NewExactName(goacodegen.NameFunction, "NewEndpoints"),
		goacodegen.NewExactName(goacodegen.NameFunction, "NewClient"),
	}
	for _, declaration := range names {
		if err := adapterPackage.DeclareName(declaration); err != nil {
			return err
		}
	}
	return nil
}

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
			if mcpRoot.HasMCP(svc) {
				mcp := mcpRoot.GetMCP(svc)
				prompts := append([]*mcpexpr.DynamicPromptExpr(nil), mcpRoot.DynamicPrompts[svc.Name]...)
				if err := validatePureMCPService(svc, mcp, prompts, source); err != nil {
					return nil, err
				}

				builder := newMCPExprBuilder(svc, mcp, prompts)
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
				_, protocolTypes := builder.Attach(
					r,
					mcpService,
					source.jsonrpcRoutes[svc.Name].path,
				)

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
					prompts:     prompts,
					mapping:     builder.BuildServiceMapping(),
				})
			}
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
