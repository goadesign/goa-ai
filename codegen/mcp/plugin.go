// Package codegen adds MCP services before Goa chooses Go names, then writes
// files that register MCP tools, call the user service, and enforce MCP's HTTP
// rules.
package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goacodegen "goa.design/goa/v3/codegen"
	goagenerator "goa.design/goa/v3/codegen/generator"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

const (
	anyTypeName      = "any"
	codecPackageName = "mcpcodec"
)

type (
	// preparedMCPService stores the design root, user service, and attached MCP
	// service for one user service.
	preparedMCPService struct {
		root        *expr.RootExpr
		userService *expr.ServiceExpr
		mcpService  *expr.ServiceExpr
		mcp         *mcpexpr.MCPExpr
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
		).buildAdapterData()
		if err != nil {
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
// also updates the JSON-RPC server with MCP's HTTP request checks.
func (p *mcpPlugin) generate(plan *goagenerator.Plan, files []*goacodegen.File) ([]*goacodegen.File, error) {
	for _, planned := range p.planned {
		services := planned.servicePlan.Services()
		mcpService := services.Get(planned.prepared.mcpService.Name)
		if mcpService == nil {
			return nil, fmt.Errorf("goa did not plan MCP service %q", planned.prepared.mcpService.Name)
		}
		userService := services.Get(planned.prepared.userService.Name)
		if userService == nil {
			return nil, fmt.Errorf("goa did not plan original service %q", planned.prepared.userService.Name)
		}
		if err := bindUserServiceMethods(services, userService, planned); err != nil {
			return nil, err
		}
		if err := bindMCPServiceMethods(planned); err != nil {
			return nil, err
		}
		if err := bindMCPJSONRPCMethods(plan, planned); err != nil {
			return nil, err
		}
		if err := planned.adapterData.jsonrpcServerImports.Link(); err != nil {
			return nil, fmt.Errorf("link MCP JSON-RPC server imports: %w", err)
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
		if session := clientSessionFile(planned.adapterData); session != nil {
			files = append(files, session)
		}
		if caller := clientCallerFile(planned.adapterData); caller != nil {
			files = append(files, caller)
		}
		files = append(files, generateMCPTransport(
			services.GenPkg(),
			planned.prepared.userService,
			planned.adapterData,
		)...)
	}
	if err := applyMCPHTTPRulesToJSONRPCMount(files, p.planned); err != nil {
		return nil, err
	}
	return removeMCPGeneratedClientCommands(p.planned, files), nil
}

// removeMCPGeneratedClientCommands removes Goa's stateless command parsers for
// MCP services. An MCP operation requires an initialized session, which one
// generated command invocation cannot provide.
func removeMCPGeneratedClientCommands(
	services []*plannedMCPService,
	files []*goacodegen.File,
) []*goacodegen.File {
	paths := make(map[string]struct{}, len(services)*2)
	for _, service := range services {
		paths[filepath.ToSlash(filepath.Join(
			goacodegen.Gendir,
			"jsonrpc",
			service.adapterData.mcpPathName,
			"client",
			"cli.go",
		))] = struct{}{}
		for _, server := range service.prepared.root.API.Servers {
			if !slices.Contains(server.Services, service.prepared.mcpService.Name) {
				continue
			}
			paths[filepath.ToSlash(filepath.Join(
				goacodegen.Gendir,
				"jsonrpc",
				"cli",
				goacodegen.SnakeCase(goacodegen.Goify(server.Name, true)),
				"cli.go",
			))] = struct{}{}
		}
	}
	kept := files[:0]
	for _, file := range files {
		if _, remove := paths[filepath.ToSlash(file.Path)]; !remove {
			kept = append(kept, file)
		}
	}
	return kept
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

// bindMCPJSONRPCMethods copies the exact request helper names used to send the
// internal initialized notification without a JSON-RPC response.
func bindMCPJSONRPCMethods(plan *goagenerator.Plan, planned *plannedMCPService) error {
	jsonrpcPlan, ok := plan.JSONRPC(planned.prepared.root)
	if !ok {
		return fmt.Errorf("goa did not plan JSON-RPC for MCP service %q", planned.prepared.mcpService.Name)
	}
	var transport *expr.HTTPServiceExpr
	for _, service := range planned.prepared.root.API.JSONRPC.Services {
		if service.ServiceExpr == planned.prepared.mcpService {
			transport = service
			break
		}
	}
	if transport == nil {
		return fmt.Errorf("goa did not plan JSON-RPC transport for MCP service %q", planned.prepared.mcpService.Name)
	}
	service, ok := jsonrpcPlan.Service(transport)
	if !ok {
		return fmt.Errorf("goa did not link JSON-RPC service %q", planned.prepared.mcpService.Name)
	}
	for _, endpoint := range service.Endpoints {
		if endpoint.Method.Name != "notifications/initialized" {
			continue
		}
		if endpoint.RequestInit == nil || endpoint.RequestEncoderDeclaration == nil {
			return fmt.Errorf("goa did not plan the initialized notification request")
		}
		planned.adapterData.ClientSession.InitializedRequestBuilder = endpoint.RequestInit.Declaration.Name()
		planned.adapterData.ClientSession.InitializedRequestEncoder = endpoint.RequestEncoderDeclaration.Name()
		return nil
	}
	return fmt.Errorf("goa did not plan JSON-RPC method %q", "notifications/initialized")
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
		goacodegen.NewImport("goa", "goa.design/goa/v3/pkg"),
	}
	data.serverImportPaths = []string{
		"context",
		"encoding/json",
		data.serviceImportPath,
		"goa.design/goa/v3/pkg",
	}
	if data.NeedsNoArgumentsValidation {
		serverFixed = append(serverFixed, goacodegen.SimpleImport("fmt"))
		data.serverImportPaths = append(data.serverImportPaths, "fmt")
	}
	if err := requireImports(data.mcpPackage, serverFixed); err != nil {
		return fmt.Errorf("plan MCP server imports: %w", err)
	}
	if err := data.mcpPackage.ReserveGeneratedImport(data.serviceGeneratedImport); err != nil {
		return fmt.Errorf("plan MCP server service import: %w", err)
	}
	if data.NeedsServerCodec {
		if err := data.mcpPackage.ReserveGeneratedImport(goacodegen.NewImport(codecPackageName, data.CodecImportPath)); err != nil {
			return fmt.Errorf("plan MCP server codec import: %w", err)
		}
		data.serverImportPaths = append(data.serverImportPaths, data.CodecImportPath)
	}

	if data.Register != nil {
		if err := planMCPRegisterTypeImports(generation, prepared, data); err != nil {
			return err
		}
		register := []*goacodegen.ImportSpec{
			goacodegen.SimpleImport("context"),
			goacodegen.SimpleImport("encoding/json"),
			goacodegen.SimpleImport("errors"),
			goacodegen.SimpleImport("strings"),
			goacodegen.NewImport("planner", "goa.design/goa-ai/runtime/agent/planner"),
			goacodegen.NewImport("policy", "goa.design/goa-ai/runtime/agent/policy"),
			goacodegen.NewImport("rawjson", "goa.design/goa-ai/runtime/agent/rawjson"),
			goacodegen.NewImport("agentsruntime", "goa.design/goa-ai/runtime/agent/runtime"),
			goacodegen.NewImport("tools", "goa.design/goa-ai/runtime/agent/tools"),
			goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
		}
		for _, spec := range register {
			data.registerImportPaths = append(data.registerImportPaths, spec.Path)
		}
		if err := requireImports(data.mcpPackage, register); err != nil {
			return fmt.Errorf("plan MCP registry imports: %w", err)
		}
		if data.NeedsRegisterCodec {
			if err := data.mcpPackage.ReserveGeneratedImport(goacodegen.NewImport(codecPackageName, data.CodecImportPath)); err != nil {
				return fmt.Errorf("plan MCP registry codec import: %w", err)
			}
			data.registerImportPaths = append(data.registerImportPaths, data.CodecImportPath)
		}
	}
	if err := planMCPSessionImports(data); err != nil {
		return err
	}
	if err := planMCPCallerImports(data); err != nil {
		return err
	}
	return planMCPJSONRPCServerImports(generation, data)
}

// planMCPJSONRPCServerImports records the extra packages named by the MCP
// request checks that replace Goa's ordinary JSON-RPC mount. Goa already owns
// the JSON-RPC package used by the original mount.
func planMCPJSONRPCServerImports(generation *goacodegen.Generation, data *AdapterData) error {
	data.jsonrpcServerImports = goacodegen.NewGeneratedImportPlan(generation.Package(path.Join(
		generation.GenPkg(),
		"jsonrpc",
		data.mcpPathName,
		"server",
	)))
	fixed := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("bytes"),
		goacodegen.SimpleImport("errors"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("io"),
		goacodegen.SimpleImport("net/http"),
		goacodegen.NewImport("goahttp", "goa.design/goa/v3/http"),
	}
	if err := data.jsonrpcServerImports.Require(fixed...); err != nil {
		return fmt.Errorf("plan MCP JSON-RPC server imports: %w", err)
	}
	if err := data.jsonrpcServerImports.AddGenerated(data.mcpGeneratedImport); err != nil {
		return fmt.Errorf("plan MCP JSON-RPC service import: %w", err)
	}
	return nil
}

// planMCPRegisterTypeImports submits every package written in a generated tool
// payload or result type before Goa chooses final import names.
func planMCPRegisterTypeImports(
	generation *goacodegen.Generation,
	prepared *preparedMCPService,
	data *AdapterData,
) error {
	for _, tool := range prepared.mcp.Tools {
		for _, attribute := range []*expr.AttributeExpr{tool.Method.Payload, tool.Method.Result} {
			if !hasMCPValue(attribute) {
				continue
			}
			if err := planMCPRegisterAttributeImports(generation, data, attribute); err != nil {
				return fmt.Errorf("plan MCP tool type import for method %q: %w", tool.Method.Name, err)
			}
		}
	}
	return nil
}

// planMCPRegisterAttributeImports walks the anonymous part of one service type.
// Named types stop the walk because their child imports belong to their own
// generated files, while arrays, maps, objects, and unions are written directly
// in register.go and need every child package there.
func planMCPRegisterAttributeImports(
	generation *goacodegen.Generation,
	data *AdapterData,
	attribute *expr.AttributeExpr,
) error {
	if _, spec := goacodegen.GetMetaType(attribute); spec != nil {
		if spec.Path == data.mcpPackage.ImportPath() {
			return nil
		}
		if err := data.mcpPackage.DeclareImport(goacodegen.NewImport(spec.Name, spec.Path)); err != nil {
			return err
		}
		data.registerImportPaths = append(data.registerImportPaths, spec.Path)
		return nil
	}
	switch actual := attribute.Type.(type) {
	case expr.UserType:
		location := goacodegen.UserTypeLocation(actual)
		if location == nil {
			data.registerImportPaths = append(data.registerImportPaths, data.serviceImportPath)
			return nil
		}
		importPath := path.Join(generation.GenPkg(), location.RelImportPath)
		if importPath == data.mcpPackage.ImportPath() {
			return nil
		}
		if err := data.mcpPackage.ReserveGeneratedImport(goacodegen.NewImport(location.PackageName(), importPath)); err != nil {
			return err
		}
		data.registerImportPaths = append(data.registerImportPaths, importPath)
	case *expr.Array:
		return planMCPRegisterAttributeImports(generation, data, actual.ElemType)
	case *expr.Map:
		if err := planMCPRegisterAttributeImports(generation, data, actual.KeyType); err != nil {
			return err
		}
		return planMCPRegisterAttributeImports(generation, data, actual.ElemType)
	case *expr.Object:
		for _, field := range *actual {
			if err := planMCPRegisterAttributeImports(generation, data, field.Attribute); err != nil {
				return err
			}
		}
	case *expr.Union:
		for _, branch := range actual.Values {
			if err := planMCPRegisterAttributeImports(generation, data, branch.Attribute); err != nil {
				return err
			}
		}
	}
	return nil
}

// planMCPSessionImports submits the imports used by the shared initialize
// helper in the generated JSON-RPC client package.
func planMCPSessionImports(data *AdapterData) error {
	pkg := data.ClientSession.clientPackage
	fixed := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"),
		goacodegen.SimpleImport("errors"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("io"),
		goacodegen.SimpleImport("net/http"),
		goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
	}
	for _, spec := range fixed {
		data.ClientSession.clientImportPaths = append(data.ClientSession.clientImportPaths, spec.Path)
	}
	data.ClientSession.clientImportPaths = append(
		data.ClientSession.clientImportPaths,
		data.mcpImportPath,
		"goa.design/goa/v3/jsonrpc",
	)
	if err := requireImports(pkg, fixed); err != nil {
		return fmt.Errorf("plan MCP session fixed imports: %w", err)
	}
	if err := pkg.DeclareImport(goacodegen.NewImport("genjsonrpc", "goa.design/goa/v3/jsonrpc")); err != nil {
		return fmt.Errorf("plan MCP session JSON-RPC import: %w", err)
	}
	if err := pkg.ReserveGeneratedImport(data.mcpGeneratedImport); err != nil {
		return fmt.Errorf("plan MCP session service import: %w", err)
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
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("sync"),
		goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
	}
	for _, spec := range fixed {
		data.ClientCaller.clientImportPaths = append(data.ClientCaller.clientImportPaths, spec.Path)
	}
	data.ClientCaller.clientImportPaths = append(data.ClientCaller.clientImportPaths, data.mcpImportPath)
	if err := requireImports(pkg, fixed); err != nil {
		return fmt.Errorf("plan MCP caller fixed imports: %w", err)
	}
	if err := pkg.ReserveGeneratedImport(data.mcpGeneratedImport); err != nil {
		return fmt.Errorf("plan MCP caller service import: %w", err)
	}
	return nil
}

// bindMCPImports reads the exact import names Goa chose for every MCP output.
func bindMCPImports(data *AdapterData) {
	data.serverImports = packageImports(data.mcpPackage, data.serverImportPaths)
	data.registerImports = packageImports(data.mcpPackage, data.registerImportPaths)
	data.Package = data.mcpPackage.ImportName(data.serviceImportPath)
	if data.NeedsServerCodec || data.NeedsRegisterCodec {
		data.CodecPackage = data.mcpPackage.ImportName(data.CodecImportPath)
	}
	if data.ClientCaller != nil {
		data.ClientCaller.imports = packageImports(data.ClientCaller.clientPackage, data.ClientCaller.clientImportPaths)
		data.ClientCaller.MCPPackage = data.ClientCaller.clientPackage.ImportName(data.mcpImportPath)
	}
	data.ClientSession.imports = packageImports(data.ClientSession.clientPackage, data.ClientSession.clientImportPaths)
	data.ClientSession.MCPPackage = data.ClientSession.clientPackage.ImportName(data.mcpImportPath)
	data.ClientSession.JSONRPCPackage = data.ClientSession.clientPackage.ImportName("goa.design/goa/v3/jsonrpc")
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

// bindMCPServiceMethods confirms that the generated service contains the
// notification used to complete initialization.
func bindMCPServiceMethods(planned *plannedMCPService) error {
	methodExpr := planned.prepared.mcpService.Method("notifications/initialized")
	if methodExpr == nil {
		return fmt.Errorf("generated MCP service has no method %q", "notifications/initialized")
	}
	return nil
}

// bindUserServiceMethods copies each original method's final selector, payload
// type, and result type after Goa has chosen package and declaration names.
func bindUserServiceMethods(
	services *goaservice.ServicesData,
	service *goaservice.Data,
	planned *plannedMCPService,
) error {
	data := planned.adapterData
	attributor := services.ServiceAttributor(service.Name, data.mcpImportPath)
	var err error
	for _, tool := range data.Tools {
		var method *goaservice.MethodData
		method, tool.PayloadType, tool.ResultType, err = bindUserServiceMethod(
			attributor,
			service,
			planned.prepared.userService,
			tool.userMethodName,
		)
		if err != nil {
			return err
		}
		tool.ServiceMethodName = method.VarName
	}
	for _, resource := range data.Resources {
		var method *goaservice.MethodData
		method, _, _, err = bindUserServiceMethod(
			attributor,
			service,
			planned.prepared.userService,
			resource.userMethodName,
		)
		if err != nil {
			return err
		}
		resource.ServiceMethodName = method.VarName
	}
	if data.Register != nil {
		for index, tool := range data.Tools {
			payloadType := tool.PayloadType
			if payloadType == "" {
				payloadType = anyTypeName
			}
			resultType := tool.ResultType
			if resultType == "" {
				resultType = anyTypeName
			}
			data.Register.Tools[index].PayloadType = payloadType
			data.Register.Tools[index].ResultType = resultType
		}
	}
	return nil
}

// bindUserServiceMethod returns Goa's final method record and its payload and
// result types as named from the generated MCP package.
func bindUserServiceMethod(
	attributor goacodegen.Attributor,
	service *goaservice.Data,
	serviceExpr *expr.ServiceExpr,
	methodName string,
) (*goaservice.MethodData, string, string, error) {
	method := service.Method(methodName)
	if method == nil {
		return nil, "", "", fmt.Errorf("goa did not plan original service method %q", methodName)
	}
	methodExpr := serviceExpr.Method(method.Name)
	if methodExpr == nil {
		return nil, "", "", fmt.Errorf("goa service expression has no method %q", methodName)
	}
	var payloadType string
	if hasMCPValue(methodExpr.Payload) {
		if method.PayloadRef == "" {
			return nil, "", "", fmt.Errorf("goa method %q has no planned payload type", methodName)
		}
		payloadType = attributor.Ref(methodExpr.Payload, "")
	}
	var resultType string
	if hasMCPValue(methodExpr.Result) {
		if method.ResultRef == "" {
			return nil, "", "", fmt.Errorf("goa method %q has no planned result type", methodName)
		}
		resultType = attributor.Ref(methodExpr.Result, "")
	}
	return method, payloadType, resultType, nil
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
	toolMethods := make(map[string]*ToolAdapter, len(data.Tools))
	for _, tool := range data.Tools {
		toolMethods[tool.userMethodName] = tool
	}
	resourceMethods := make(map[string]*ResourceAdapter, len(data.Resources))
	for _, resource := range data.Resources {
		resourceMethods[resource.userMethodName] = resource
	}
	for _, method := range methods {
		values := new(plannedMethodCodec)
		preferred := goacodegen.Goify(method.Name, true)
		payloadDirection, resultDirection := mcpCodecDirections(prepared, method.Name)
		if tool := toolMethods[method.Name]; tool != nil {
			data.NeedsServerCodec = data.NeedsServerCodec || tool.HasPayload || tool.HasResult && !tool.TextResult
			data.NeedsRegisterCodec = data.NeedsRegisterCodec || tool.HasPayload || tool.HasResult
		}
		if resource := resourceMethods[method.Name]; resource != nil {
			data.NeedsServerCodec = data.NeedsServerCodec || !resource.TextResult
		}
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
	data.CodecPackage = codecPackageName
	return planned, methodCodecs, nil
}

// bindMCPCodecs joins codec types to Goa's final service declarations and adds
// the chosen function names to the server and tool registration data.
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
	files, err := planned.codecPlan.Files()
	if err != nil {
		return nil, fmt.Errorf("render MCP codecs for service %q: %w", planned.prepared.userService.Name, err)
	}
	return files, nil
}

// bindMCPCodecData copies final generated function names to each mapped MCP
// method and records which generated files import the private codec package.
func bindMCPCodecData(data *AdapterData, methods map[string]*plannedMethodCodec) {
	for index, tool := range data.Tools {
		tool.Codec = methodCodecData(methods[tool.userMethodName])
		if data.Register != nil {
			data.Register.Tools[index].Codec = tool.Codec
		}
	}
	for _, resource := range data.Resources {
		resource.Codec = methodCodecData(methods[resource.userMethodName])
	}
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
			resultEncode = true
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
	if data.NeedsBoolPtr {
		if err := mcpPackage.DeclareName(goacodegen.NewExactName(
			goacodegen.NameFunction,
			"boolPtr",
		)); err != nil {
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
	for _, declaration := range []*goacodegen.NameDeclaration{
		goacodegen.NewExactName(goacodegen.NameType, "mcpResponseWriter"),
		goacodegen.NewExactName(goacodegen.NameFunction, "MountWithOrigins"),
		goacodegen.NewExactName(goacodegen.NameFunction, "withMCPTransport"),
		goacodegen.NewExactName(goacodegen.NameFunction, "mcpGETHandler"),
		goacodegen.NewExactName(goacodegen.NameFunction, "mcpOriginAllowed"),
	} {
		if err := serverPackage.DeclareName(declaration); err != nil {
			return err
		}
	}
	return nil
}

// declareMCPClientNames reserves the Go names written by MCP client files.
func declareMCPClientNames(generation *goacodegen.Generation, data *AdapterData) error {
	clientPackage, err := generation.ClaimPackage(path.Join(
		generation.GenPkg(),
		"jsonrpc/"+data.mcpPathName+"/client",
	))
	if err != nil {
		return err
	}
	data.ClientSession.clientPackage = clientPackage
	for _, declaration := range []*goacodegen.NameDeclaration{
		goacodegen.NewExactName(goacodegen.NameFunction, "InitializeSession"),
		goacodegen.NewExactName(goacodegen.NameFunction, "initializeSession"),
		goacodegen.NewExactName(goacodegen.NameFunction, "callerError"),
	} {
		if err := clientPackage.DeclareName(declaration); err != nil {
			return err
		}
	}
	if data.ClientCaller != nil {
		data.ClientCaller.clientPackage = clientPackage
		for _, declaration := range []*goacodegen.NameDeclaration{
			goacodegen.NewExactName(goacodegen.NameType, "Caller"),
			goacodegen.NewExactName(goacodegen.NameFunction, "NewCaller"),
		} {
			if err := clientPackage.DeclareName(declaration); err != nil {
				return err
			}
		}
	}
	return nil
}
