// Package codegen adds MCP services before Goa chooses Go names, then writes
// files that register MCP tools, call the user service, and enforce MCP's HTTP
// rules.
package codegen

import (
	"fmt"
	"path"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goacodegen "goa.design/goa/v3/codegen"
	goagenerator "goa.design/goa/v3/codegen/generator"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

const (
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

	// plannedMethodCodec keeps each method side's codec and the exact Go layout
	// supplied to it. Registration uses that layout for type-reference imports.
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
			prepared.root.API,
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
		codecPlan, methodCodecs, err := planMCPCodecs(plan.Generation(), servicePlan, prepared, adapter)
		if err != nil {
			return err
		}
		if err := planMCPImports(plan.Generation(), adapter); err != nil {
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
		if err := bindUserServiceMethods(userService, planned); err != nil {
			return nil, err
		}
		if err := planned.adapterData.jsonrpcClientImports.Link(); err != nil {
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
	if err := applyMCPContentValidation(files, p.planned); err != nil {
		return nil, err
	}
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

// planMCPImports submits every package name used by MCP files before Goa
// chooses import names for their output packages.
func planMCPImports(
	generation *goacodegen.Generation,
	data *AdapterData,
) error {
	data.jsonrpcClientImportPath = path.Join(generation.GenPkg(), "jsonrpc", data.mcpPathName, "client")
	data.mcpPackage = generation.Package(data.mcpImportPath)

	serverFixed := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"),
		goacodegen.SimpleImport("encoding/json"),
		goacodegen.NewImport("goa", "goa.design/goa/v3/pkg"),
		goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
		goacodegen.SimpleImport("go.opentelemetry.io/otel"),
		goacodegen.SimpleImport("go.opentelemetry.io/otel/codes"),
	}
	data.serverImportPaths = []string{
		"context",
		"encoding/json",
		data.serviceImportPath,
		"goa.design/goa/v3/pkg",
		"goa.design/goa-ai/runtime/mcp",
		"go.opentelemetry.io/otel",
		"go.opentelemetry.io/otel/codes",
	}
	for _, resource := range data.Resources {
		if !resource.BinaryResult {
			continue
		}
		serverFixed = append(serverFixed, goacodegen.SimpleImport("encoding/base64"))
		data.serverImportPaths = append(data.serverImportPaths, "encoding/base64")
		break
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

	if err := planMCPCallerImports(data); err != nil {
		return err
	}
	if err := planMCPJSONRPCClientImports(generation, data); err != nil {
		return err
	}
	return planMCPJSONRPCServerImports(generation, data)
}

// planMCPJSONRPCClientImports reserves the shared HTTP binding before Goa
// chooses names for the generated client's constructor.
func planMCPJSONRPCClientImports(generation *goacodegen.Generation, data *AdapterData) error {
	data.jsonrpcClientImports = goacodegen.NewGeneratedImportPlan(generation.Package(data.jsonrpcClientImportPath))
	return data.jsonrpcClientImports.Require(goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"))
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
		goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
	}
	if err := data.jsonrpcServerImports.Require(fixed...); err != nil {
		return fmt.Errorf("plan MCP JSON-RPC server imports: %w", err)
	}
	if err := data.jsonrpcServerImports.AddGenerated(data.mcpGeneratedImport); err != nil {
		return fmt.Errorf("plan MCP JSON-RPC service import: %w", err)
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
	data.Package = data.mcpPackage.ImportName(data.serviceImportPath)
	if data.NeedsServerCodec {
		data.CodecPackage = data.mcpPackage.ImportName(data.CodecImportPath)
	}
	if data.ClientCaller != nil {
		data.ClientCaller.imports = packageImports(data.ClientCaller.clientPackage, data.ClientCaller.clientImportPaths)
		data.ClientCaller.MCPPackage = data.ClientCaller.clientPackage.ImportName(data.mcpImportPath)
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

// bindUserServiceMethods copies the original selectors after Goa assigns names.
func bindUserServiceMethods(service *goaservice.Data, planned *plannedMCPService) error {
	for _, tool := range planned.adapterData.Tools {
		method := service.Method(tool.userMethodName)
		if method == nil {
			return fmt.Errorf("goa did not plan tool method %q", tool.userMethodName)
		}
		tool.ServiceMethodName = method.VarName
	}
	for _, resource := range planned.adapterData.Resources {
		method := service.Method(resource.userMethodName)
		if method == nil {
			return fmt.Errorf("goa did not plan resource method %q", resource.userMethodName)
		}
		resource.ServiceMethodName = method.VarName
	}
	return nil
}

// planMCPCodecs records the private JSON package and every mapped service value
// before Goa chooses final Go names.
func planMCPCodecs(
	generation *goacodegen.Generation,
	services *goaservice.Plan,
	prepared *preparedMCPService,
	data *AdapterData,
) (*jsoncodec.Plan, map[string]*plannedMethodCodec, error) {
	methods := mappedMCPMethods(prepared)
	hasValues := false
	for _, method := range methods {
		payloadDirection, resultDirection := mcpCodecDirections(data, method.Name)
		if (hasMCPValue(method.Payload) && payloadDirection != 0) || (hasMCPValue(method.Result) && resultDirection != 0) {
			hasValues = true
			break
		}
	}
	if !hasValues {
		return nil, nil, nil
	}

	codecImportPath := path.Join(data.mcpImportPath, "internal", "codec")
	planned, err := jsoncodec.NewPlan(generation, codecImportPath)
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
		payloadDirection, resultDirection := mcpCodecDirections(data, method.Name)
		if tool := toolMethods[method.Name]; tool != nil {
			data.NeedsServerCodec = data.NeedsServerCodec || tool.HasPayload || tool.HasResult
		}
		if resource := resourceMethods[method.Name]; resource != nil {
			data.NeedsServerCodec = data.NeedsServerCodec || (!resource.TextResult && !resource.BinaryResult)
		}
		if hasMCPValue(method.Payload) && payloadDirection != 0 {
			layout, layoutErr := services.MethodTypeLayout(method, method.Payload)
			if layoutErr != nil {
				return nil, nil, fmt.Errorf("plan MCP payload layout for method %q: %w", method.Name, layoutErr)
			}
			values.payload, err = planned.Add(
				prepared.userService.Name+":"+method.Name+":payload",
				preferred+"Payload",
				method.Payload,
				layout,
				payloadDirection,
			)
			if err != nil {
				return nil, nil, fmt.Errorf("plan MCP payload codec for method %q: %w", method.Name, err)
			}
		}
		if hasMCPValue(method.Result) && resultDirection != 0 {
			layout, layoutErr := services.MethodTypeLayout(method, method.Result)
			if layoutErr != nil {
				return nil, nil, fmt.Errorf("plan MCP result layout for method %q: %w", method.Name, layoutErr)
			}
			values.result, err = planned.Add(
				prepared.userService.Name+":"+method.Name+":result",
				preferred+"Result",
				method.Result,
				layout,
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
	files, err := planned.codecPlan.Files("codec")
	if err != nil {
		return nil, fmt.Errorf("render MCP codecs for service %q: %w", planned.prepared.userService.Name, err)
	}
	return files, nil
}

// bindMCPCodecData copies final generated function names to each mapped MCP
// method and records which generated files import the private codec package.
func bindMCPCodecData(data *AdapterData, methods map[string]*plannedMethodCodec) {
	for _, tool := range data.Tools {
		tool.Codec = methodCodecData(methods[tool.userMethodName])
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
func mcpCodecDirections(data *AdapterData, methodName string) (jsoncodec.Direction, jsoncodec.Direction) {
	var payloadEncode, payloadDecode, resultEncode, resultDecode bool
	for _, tool := range data.Tools {
		if tool.userMethodName == methodName {
			payloadEncode, payloadDecode = true, true
			resultEncode, resultDecode = true, true
		}
	}
	for _, resource := range data.Resources {
		if resource.userMethodName == methodName && !resource.TextResult && !resource.BinaryResult {
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
		goacodegen.NewExactName(goacodegen.NameFunction, "resultMeta"),
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
	if len(data.Tools) > 0 {
		if err := mcpPackage.DeclareName(goacodegen.NewExactName(
			goacodegen.NameFunction,
			"toolCallError",
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
		goacodegen.NewExactName(goacodegen.NameFunction, "mcpMethodNotAllowed"),
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
