// Package codegen turns Goa and Goa-AI designs into generated agent code.
//
// This file records the generated packages and the tools or completions that
// belong to each package before later planning chooses imports, names, and type
// conversions.
package codegen

import (
	"fmt"
	"path"
	"slices"

	"goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// toolSpecsPlan stores the public and HTTP packages for every toolset and
	// completion service, plus the Goa API and MCP design used to build them.
	toolSpecsPlan struct {
		byDir       map[string]*toolSpecsPackagePlan
		completions map[string]*toolSpecsPackagePlan
		genpkg      string
		api         *goaexpr.APIExpr
		mcp         *mcpexpr.RootExpr
		service     *service.Plan
	}

	// toolSpecsPackagePlan stores the public specs package and the HTTP helper
	// package written for one toolset.
	toolSpecsPackagePlan struct {
		generation             *goacodegen.Generation
		definition             *ir.Toolset
		genpkg                 string
		public                 *goacodegen.GeneratedPackage
		transport              *goacodegen.GeneratedPackage
		types                  map[specTypeKey]*plannedSpecType
		publicTypes            map[goaexpr.UserType]*goacodegen.TypeDeclaration
		transportTypes         map[goaexpr.UserType]*goacodegen.TypeDeclaration
		publicTypeUses         map[goaexpr.UserType]*goacodegen.NameDeclaration
		transportTypeUses      map[goaexpr.UserType]*goacodegen.NameDeclaration
		transportValidators    map[goaexpr.UserType]*goacodegen.NameDeclaration
		publicFixed            map[string]*goacodegen.NameDeclaration
		transportFixed         map[string]*goacodegen.NameDeclaration
		publicUnionErrors      map[goacodegen.UnionDeclarationID]*goacodegen.NameDeclaration
		transportUnionErrors   map[goacodegen.UnionDeclarationID]*goacodegen.NameDeclaration
		jsonValidatorGraphs    []*plannedJSONValidatorGraph
		jsonDocumentValidators []*plannedJSONValidatorGraph
		jsonValidators         []*plannedJSONValidator
		tools                  map[string]*plannedToolNames
		completionNames        map[string]*plannedCompletionNames
		transformPlans         []*plannedPackageTransform
		adapterTransformPlans  []*plannedPackageTransform
		specs                  *toolSpecsData
		completion             *completionSpecsData
		fileImports            *toolSpecsFileImports
		providerImportPaths    []string
		transformImportPaths   []string
		serviceImportPath      string
		registrationRoutes     []string
		render                 *ToolsetData
		registry               bool
	}

	// toolSpecsFileImports keeps the imports used by each generated file. Goa
	// chooses their final package names after every file has recorded its needs.
	// Registry toolsets write no transport files, so their transport plans are nil.
	toolSpecsFileImports struct {
		publicTypes       *goacodegen.GeneratedImportPlan
		publicCodecs      *goacodegen.GeneratedImportPlan
		publicUnions      *goacodegen.GeneratedImportPlan
		publicSpecs       *goacodegen.GeneratedImportPlan
		publicInject      *goacodegen.GeneratedImportPlan
		transportTypes    *goacodegen.GeneratedImportPlan
		transportValidate *goacodegen.GeneratedImportPlan
		transportUnions   *goacodegen.GeneratedImportPlan
	}

	// plannedSpecType stores one public Go type, its JSON-decoding Go type, and
	// the functions that copy values between them.
	plannedSpecType struct {
		publicDeclaration    *goacodegen.NameDeclaration
		transportDeclaration *goacodegen.NameDeclaration
		publicLayout         *goacodegen.GoTypePlan
		transportLayout      *goacodegen.GoTypePlan
		publicShape          *goaexpr.AttributeExpr
		transportShape       *goaexpr.AttributeExpr
		publicTypes          []*localizedType
		transportTypes       []*localizedType
		public               *goaexpr.AttributeExpr
		transport            *goaexpr.AttributeExpr
		decode               *goacodegen.TransformPlan
		encode               *goacodegen.TransformPlan
		exportedCodec        *goacodegen.NameDeclaration
		genericCodec         *goacodegen.NameDeclaration
		marshal              *goacodegen.NameDeclaration
		unmarshal            *goacodegen.NameDeclaration
		transportValidator   *goacodegen.NameDeclaration
		fieldDescriptions    *goacodegen.NameDeclaration
		fieldJSONTypes       *goacodegen.NameDeclaration
		enrichValidation     *goacodegen.NameDeclaration
		invalidFieldType     *goacodegen.NameDeclaration
		jsonValidator        *plannedJSONValidatorGraph
	}

	// plannedJSONValidatorGraph stores the private functions that validate one
	// raw JSON document and every known value shape reachable from it.
	plannedJSONValidatorGraph struct {
		document  *goacodegen.NameDeclaration
		key       string
		preferred string
		role      string
		root      *plannedJSONValidator
		render    bool
	}

	// plannedJSONValidator stores one raw JSON value shape and its generated
	// function name. Recursive named types point back to an existing value.
	plannedJSONValidator struct {
		declaration     *goacodegen.NameDeclaration
		key             string
		preferred       string
		role            string
		kind            string
		expected        string
		signedInteger   bool
		unsignedInteger bool
		integerBits     int
		fields          []*plannedJSONValidatorField
		element         *plannedJSONValidatorCall
	}

	// plannedJSONValidatorField stores one accepted object field and the check
	// applied to its value. A nil check accepts the value unchanged.
	plannedJSONValidatorField struct {
		name string
		call *plannedJSONValidatorCall
	}

	// plannedJSONValidatorCall links a child value to its generated validator.
	plannedJSONValidatorCall struct {
		validator          *plannedJSONValidator
		description        string
		inheritDescription bool
	}

	// localizedType keeps the Goa type that owns a generated nested type next to
	// the copy written in the tool package.
	localizedType struct {
		source      goaexpr.UserType
		generated   *goaexpr.UserTypeExpr
		declaration *goacodegen.TypeDeclaration
	}

	// localizedSpecTypeShapes stores the public and JSON-decoding shapes used by
	// one generated tool type. Import planning and type declaration both use
	// these shapes so they cannot disagree about which packages the type needs.
	localizedSpecTypeShapes struct {
		public         *goaexpr.AttributeExpr
		publicTypes    []*localizedType
		transport      *goaexpr.AttributeExpr
		transportTypes []*localizedType
		usesTransport  bool
	}

	// plannedToolNames stores every name written for one tool.
	plannedToolNames struct {
		constant                   *goacodegen.NameDeclaration
		constructor                *goacodegen.NameDeclaration
		spec                       *goacodegen.NameDeclaration
		typed                      *goacodegen.NameDeclaration
		inject                     *goacodegen.NameDeclaration
		decode                     *goacodegen.NameDeclaration
		canonicalizeServerData     *goacodegen.NameDeclaration
		canonicalizeServerDataItem *goacodegen.NameDeclaration
		methodPayloadTransform     *goacodegen.NameDeclaration
		toolResultTransform        *goacodegen.NameDeclaration
		serverDataTransforms       map[string]*goacodegen.NameDeclaration
		bounds                     *goacodegen.NameDeclaration
		injectedFieldLayouts       map[string]*goacodegen.GoTypePlan
		payloadType                *plannedSpecType
		resultType                 *plannedSpecType
		serverDataTypes            map[string]*plannedSpecType
		methodPayloadTransformPlan *goacodegen.TransformPlan
		toolResultTransformPlan    *goacodegen.TransformPlan
		serverDataTransformPlans   map[string]*goacodegen.TransformPlan
	}

	// plannedCompletionNames stores every name written for one completion.
	plannedCompletionNames struct {
		constant       *goacodegen.NameDeclaration
		spec           *goacodegen.NameDeclaration
		example        *goacodegen.NameDeclaration
		complete       *goacodegen.NameDeclaration
		streamComplete *goacodegen.NameDeclaration
		resultType     *plannedSpecType
	}

	// specNameOrder stores the package path and declaration key used to order
	// names that request the same spelling.
	specNameOrder struct {
		packagePath string
		key         string
	}

	// transformHelperNameOrder keeps helper collisions stable when sibling
	// fields are authored in a different order.
	transformHelperNameOrder struct {
		packagePath string
		key         string
		location    goacodegen.TransformHelperDefinitionLocation
	}

	// unionErrorNameOrder stores the package and exact union name that own one
	// function for reporting an unknown OneOf branch.
	unionErrorNameOrder struct {
		packagePath string
		unionName   string
	}

	// localizedTypeNameOrder stores the stable Goa type details used to order
	// generated nested types that request the same Go name.
	localizedTypeNameOrder struct {
		packagePath string
		sourcePath  string
		sourceName  string
		sourceID    string
		role        localizedTypeNameRole
	}

	// localizedTypeNameRole distinguishes a nested type from the validation
	// function generated for that type.
	localizedTypeNameRole uint8
)

const (
	localizedTypeDeclarationName localizedTypeNameRole = iota + 1
	localizedTypeValidatorName
)

// planToolSpecs records every package name and conversion needed by generated
// tools and completions. Tool definitions already contain the types needed at
// this step, before Goa builds service data.
func planToolSpecs(
	generation *goacodegen.Generation,
	design *ir.Design,
	api *goaexpr.APIExpr,
	mcpRoot *mcpexpr.RootExpr,
) (*toolSpecsPlan, error) {
	planned := &toolSpecsPlan{
		byDir:       make(map[string]*toolSpecsPackagePlan),
		completions: make(map[string]*toolSpecsPackagePlan),
		api:         api,
		mcp:         mcpRoot,
	}
	if design == nil {
		return planned, nil
	}
	planned.genpkg = design.Genpkg
	for _, toolset := range design.Toolsets {
		if toolset == nil || toolset.Expr == nil {
			continue
		}
		tools, err := toolExpressionsForDefinition(planned.mcp, toolset)
		if err != nil {
			return nil, err
		}
		if isRegistryReference(toolset.Owner.Ref) {
			if err := planned.addRegistryPackage(generation, toolset); err != nil {
				return nil, err
			}
			continue
		}
		if len(tools) == 0 {
			continue
		}
		if err := planned.addToolPackage(
			generation,
			toolset,
			toolset.Name,
			toolset.Name,
			toolset.SpecsImportPath,
			toolset.SpecsDir,
			toolsetRegistrationRoutes(design, toolset),
			tools,
		); err != nil {
			return nil, err
		}
	}
	for _, completion := range design.Completions {
		if completion == nil || completion.Expr == nil || completion.Service == nil {
			continue
		}
		importPath := path.Join(design.Genpkg, completion.Service.PathName, "completions")
		packagePlan := planned.completions[completion.Service.Name]
		if packagePlan == nil {
			public, err := generation.ClaimPackage(importPath)
			if err != nil {
				return nil, fmt.Errorf("plan service %q completion package: %w", completion.Service.Name, err)
			}
			transport, err := generation.ClaimPackage(path.Join(importPath, "http"))
			if err != nil {
				return nil, fmt.Errorf("plan service %q completion HTTP package: %w", completion.Service.Name, err)
			}
			packagePlan = newToolSpecsPackagePlan(generation, planned.genpkg, public, transport)
			planned.completions[completion.Service.Name] = packagePlan
		}
		owner := &contractTypeOwner{
			Kind:          contractTypeOwnerCompletion,
			Name:          completion.Name,
			QualifiedName: completion.Service.Name + "." + completion.Name,
			ScopeName:     completion.Service.Name + ".completions",
		}
		if err := packagePlan.declareTypeImports(owner, completion.Expr.Return, usageResult); err != nil {
			return nil, fmt.Errorf("plan completion %q imports: %w", completion.Name, err)
		}
	}
	for serviceName, packagePlan := range planned.completions {
		if err := packagePlan.declareCompletionPackageNames(); err != nil {
			return nil, fmt.Errorf("plan service %q completion package names: %w", serviceName, err)
		}
	}
	for _, completion := range design.Completions {
		if completion == nil || completion.Expr == nil || completion.Service == nil {
			continue
		}
		packagePlan := planned.completions[completion.Service.Name]
		if err := packagePlan.declareCompletionNames(completion.Name); err != nil {
			return nil, fmt.Errorf("plan completion %q names: %w", completion.Name, err)
		}
		owner := &contractTypeOwner{
			Kind:          contractTypeOwnerCompletion,
			Name:          completion.Name,
			QualifiedName: completion.Service.Name + "." + completion.Name,
			ScopeName:     completion.Service.Name + ".completions",
		}
		if err := packagePlan.declareType(owner, completion.Expr.Return, usageResult, ""); err != nil {
			return nil, fmt.Errorf("plan completion %q: %w", completion.Name, err)
		}
		packagePlan.completionNames[completion.Name].resultType = packagePlan.types[stableTypeKey(owner, usageResult, "")]
		packagePlan.completionNames[completion.Name].resultType.jsonValidator.render = true
	}
	for serviceName, packagePlan := range planned.completions {
		if err := packagePlan.finalizeJSONValidators(); err != nil {
			return nil, fmt.Errorf("plan service %q completion JSON validators: %w", serviceName, err)
		}
		if err := packagePlan.planCompletionFileImports(); err != nil {
			return nil, fmt.Errorf("plan service %q completion file imports: %w", serviceName, err)
		}
	}
	return planned, nil
}

// toolsetRegistrationRoutes returns every generated name that may register one
// toolset definition. Several services can share the same generated schemas
// while registering them under different names.
func toolsetRegistrationRoutes(design *ir.Design, definition *ir.Toolset) []string {
	seen := make(map[string]struct{})
	add := func(reference *ir.ToolsetRef) {
		if reference == nil || reference.Definition != definition {
			return
		}
		seen[reference.QualifiedName] = struct{}{}
	}
	add(definition.Owner.Ref)
	for _, reference := range design.ServiceExports {
		add(reference)
	}
	for _, agent := range design.Agents {
		for _, reference := range agent.UsedToolsets {
			add(reference)
		}
		for _, reference := range agent.ExportedToolsets {
			add(reference)
		}
	}
	routes := make([]string, 0, len(seen))
	for route := range seen {
		routes = append(routes, route)
	}
	slices.Sort(routes)
	return routes
}

// addRegistryPackage records the fixed declarations written for a registry
// toolset whose tools are discovered after the program starts.
func (p *toolSpecsPlan) addRegistryPackage(generation *goacodegen.Generation, definition *ir.Toolset) error {
	public, err := generation.ClaimPackage(definition.SpecsImportPath)
	if err != nil {
		return fmt.Errorf("plan toolset %q registry specs package: %w", definition.Name, err)
	}
	packagePlan := newToolSpecsPackagePlan(generation, p.genpkg, public, nil)
	packagePlan.definition = definition
	packagePlan.registry = true
	names := map[goacodegen.PackageNameKind][]string{
		goacodegen.NameType:     {"RegistryClient", "ToolsetSchema", "ToolSchema"},
		goacodegen.NameConstant: {"RegistryName", "ToolsetName"},
		goacodegen.NameVariable: {"Specs", "specIndex", "metadataIndex", "metadata", "mu"},
		goacodegen.NameFunction: {
			"DiscoverAndPopulate", "Names", "Spec", "PayloadSchema", "ResultSchema",
			"Metadata", "MetadataByName", "ValidatePayload", "ValidateResult",
		},
	}
	if definition.Owner.Ref.Provider.Registry.Version != "" {
		names[goacodegen.NameConstant] = append(names[goacodegen.NameConstant], "Version")
	}
	if err := declareExactNames(public, packagePlan.publicFixed, names); err != nil {
		return fmt.Errorf("plan toolset %q registry declarations: %w", definition.Name, err)
	}
	if err := packagePlan.fileImports.publicSpecs.Require(
		goacodegen.SimpleImport("context"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("sort"),
		goacodegen.SimpleImport("sync"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/policy"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/tools"),
		goacodegen.NewImport("registryschema", "goa.design/goa-ai/runtime/toolregistry/schema"),
	); err != nil {
		return fmt.Errorf("plan toolset %q registry imports: %w", definition.Name, err)
	}
	p.byDir[definition.SpecsDir] = packagePlan
	return nil
}

// addToolPackage records all names and conversions written to one tool package.
func (p *toolSpecsPlan) addToolPackage(
	generation *goacodegen.Generation,
	definition *ir.Toolset,
	label, qualified, importPath, outputDir string,
	registrationRoutes []string,
	tools []*agent.ToolExpr,
) error {
	public, err := generation.ClaimPackage(importPath)
	if err != nil {
		return fmt.Errorf("plan toolset %q public specs package: %w", label, err)
	}
	transport, err := generation.ClaimPackage(path.Join(importPath, "http"))
	if err != nil {
		return fmt.Errorf("plan toolset %q HTTP package: %w", label, err)
	}
	packagePlan := newToolSpecsPackagePlan(generation, p.genpkg, public, transport)
	packagePlan.definition = definition
	packagePlan.registrationRoutes = registrationRoutes
	for _, tool := range tools {
		if err := packagePlan.declareToolTypeImports(qualified, tool); err != nil {
			return fmt.Errorf("plan toolset %q tool %q imports: %w", label, tool.Name, err)
		}
	}
	if err := packagePlan.declareToolPackageNames(); err != nil {
		return fmt.Errorf("plan toolset %q package names: %w", label, err)
	}
	for _, tool := range tools {
		if err := packagePlan.declareToolNames(qualified, tool); err != nil {
			return fmt.Errorf("plan toolset %q tool %q names: %w", label, tool.Name, err)
		}
		if err := packagePlan.declareToolTypes(qualified, tool); err != nil {
			return fmt.Errorf("plan toolset %q tool %q types: %w", label, tool.Name, err)
		}
		if err := packagePlan.declareToolTransforms(qualified, tool); err != nil {
			return fmt.Errorf("plan toolset %q tool %q conversions: %w", label, tool.Name, err)
		}
	}
	if err := packagePlan.finalizeJSONValidators(); err != nil {
		return fmt.Errorf("plan toolset %q JSON validators: %w", label, err)
	}
	if err := packagePlan.planToolFileImports(tools); err != nil {
		return fmt.Errorf("plan toolset %q file imports: %w", label, err)
	}
	p.byDir[outputDir] = packagePlan
	return nil
}

// toolExpressionsForReference returns the defining toolset's contracts and
// expands Goa-backed MCP methods using this reference's provider context.
func toolExpressionsForReference(mcpRoot *mcpexpr.RootExpr, reference *ir.ToolsetRef) ([]*agent.ToolExpr, error) {
	return expandToolExpressions(mcpRoot, reference.Name, reference.Definition.Expr)
}

// toolExpressionsForDefinition returns the contracts owned by one defining
// toolset. Its saved reference supplies only provider and service context.
func toolExpressionsForDefinition(mcpRoot *mcpexpr.RootExpr, toolset *ir.Toolset) ([]*agent.ToolExpr, error) {
	return toolExpressionsForReference(mcpRoot, toolset.Owner.Ref)
}

// isRegistryReference reports whether a package contains runtime registry
// declarations even though it has no compile-time tools.
func isRegistryReference(reference *ir.ToolsetRef) bool {
	return reference.Provider != nil && reference.Provider.Kind == agent.ProviderRegistry
}

// expandToolExpressions expands Goa-backed MCP methods and otherwise returns
// the defining toolset's tools unchanged.
func expandToolExpressions(mcpRoot *mcpexpr.RootExpr, name string, expr *agent.ToolsetExpr) ([]*agent.ToolExpr, error) {
	if expr.Provider == nil || expr.Provider.Kind != agent.ProviderMCP || expr.Provider.MCPSource != agent.MCPSourceGoa {
		return expr.Tools, nil
	}
	var server *mcpexpr.MCPExpr
	if mcpRoot != nil {
		server = mcpRoot.ServiceMCP(expr.Provider.MCPService, expr.Provider.MCPToolset)
	}
	if server == nil {
		return nil, fmt.Errorf(
			"toolset %q could not resolve Goa-defined MCP toolset %q on service %q",
			name,
			expr.Provider.MCPToolset,
			expr.Provider.MCPService,
		)
	}
	tools := make([]*agent.ToolExpr, 0, len(server.Tools))
	for _, tool := range server.Tools {
		planned := &agent.ToolExpr{
			Name:        tool.Name,
			Description: tool.Description,
		}
		if tool.Method != nil {
			planned.Args = tool.Method.Payload
			planned.Return = tool.Method.Result
		}
		tools = append(tools, planned)
	}
	return tools, nil
}
