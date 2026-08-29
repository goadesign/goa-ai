// Package codegen records every Go name and type conversion used by generated tool
// and completion packages before Goa chooses the final names. Public types and
// JSON-decoding types are recorded separately because they are written to
// different Go packages.
package codegen

import (
	"fmt"
	"path"
	"slices"
	"strings"

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

// newToolSpecsPackagePlan creates empty name and type maps for one public tool
// package and its HTTP decoding package.
func newToolSpecsPackagePlan(generation *goacodegen.Generation, genpkg string, public, transport *goacodegen.GeneratedPackage) *toolSpecsPackagePlan {
	return &toolSpecsPackagePlan{
		generation:           generation,
		genpkg:               genpkg,
		public:               public,
		transport:            transport,
		types:                make(map[specTypeKey]*plannedSpecType),
		publicTypes:          make(map[goaexpr.UserType]*goacodegen.TypeDeclaration),
		transportTypes:       make(map[goaexpr.UserType]*goacodegen.TypeDeclaration),
		publicTypeUses:       make(map[goaexpr.UserType]*goacodegen.NameDeclaration),
		transportTypeUses:    make(map[goaexpr.UserType]*goacodegen.NameDeclaration),
		transportValidators:  make(map[goaexpr.UserType]*goacodegen.NameDeclaration),
		publicFixed:          make(map[string]*goacodegen.NameDeclaration),
		transportFixed:       make(map[string]*goacodegen.NameDeclaration),
		publicUnionErrors:    make(map[goacodegen.UnionDeclarationID]*goacodegen.NameDeclaration),
		transportUnionErrors: make(map[goacodegen.UnionDeclarationID]*goacodegen.NameDeclaration),
		tools:                make(map[string]*plannedToolNames),
		completionNames:      make(map[string]*plannedCompletionNames),
		fileImports:          newToolSpecsFileImports(public, transport),
	}
}

// newToolSpecsFileImports creates one import record for every file this
// package can emit. Empty records do not reserve package names.
func newToolSpecsFileImports(public, transport *goacodegen.GeneratedPackage) *toolSpecsFileImports {
	imports := &toolSpecsFileImports{
		publicTypes:  goacodegen.NewGeneratedImportPlan(public),
		publicCodecs: goacodegen.NewGeneratedImportPlan(public),
		publicUnions: goacodegen.NewGeneratedImportPlan(public),
		publicSpecs:  goacodegen.NewGeneratedImportPlan(public),
		publicInject: goacodegen.NewGeneratedImportPlan(public),
	}
	if transport == nil {
		return imports
	}
	imports.transportTypes = goacodegen.NewGeneratedImportPlan(transport)
	imports.transportValidate = goacodegen.NewGeneratedImportPlan(transport)
	imports.transportUnions = goacodegen.NewGeneratedImportPlan(transport)
	return imports
}

// link reads the package names Goa selected after generation planning ended.
func (p *toolSpecsFileImports) link() error {
	plans := []*goacodegen.GeneratedImportPlan{
		p.publicTypes,
		p.publicCodecs,
		p.publicUnions,
		p.publicSpecs,
		p.publicInject,
	}
	if p.transportTypes != nil {
		plans = append(plans, p.transportTypes, p.transportValidate, p.transportUnions)
	}
	for _, plan := range plans {
		if err := plan.Link(); err != nil {
			return err
		}
	}
	return nil
}

// planToolFileImports records the fixed package names written by each emitted
// tool file. Type imports were recorded earlier from the design.
func (p *toolSpecsPackagePlan) planToolFileImports(tools []*agent.ToolExpr) error {
	if err := p.planSharedFileImports(); err != nil {
		return err
	}
	hasServerData := false
	hasInject := false
	for _, tool := range tools {
		if len(tool.ServerData) > 0 {
			hasServerData = true
		}
		if len(tool.InjectedFields) > 0 {
			hasInject = true
		}
	}
	specs := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/policy"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/tools"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/toolregistry"),
	}
	if hasServerData {
		specs = append(
			[]*goacodegen.ImportSpec{goacodegen.SimpleImport("fmt")},
			append(specs, goacodegen.SimpleImport("goa.design/goa-ai/runtime/toolserverdata"))...,
		)
	}
	if err := p.fileImports.publicSpecs.Require(specs...); err != nil {
		return err
	}
	if hasInject {
		injectImports := []*goacodegen.ImportSpec{
			goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/runtime"),
		}
		hasLabelInject := false
		for _, tool := range tools {
			payload := effectiveObject(tool.Args)
			for _, name := range tool.InjectedFields {
				if _, metaBacked := injectedFieldSource(name); metaBacked {
					continue
				}
				hasLabelInject = true
				field := payload.Attribute(name)
				for _, preference := range goacodegen.ValidationRuntimeImports(
					field,
					goacodegen.GoLayoutPolicy{UseDefault: true, SumType: true},
				) {
					injectImports = append(injectImports, goacodegen.NewImport(preference.Name, preference.Path))
				}
			}
		}
		if hasLabelInject {
			injectImports = append(injectImports, goacodegen.SimpleImport("fmt"))
		}
		if err := p.fileImports.publicInject.Require(injectImports...); err != nil {
			return err
		}
	}
	return nil
}

// planCompletionFileImports records the fixed package names written by a
// completion package after all result shapes are known.
func (p *toolSpecsPackagePlan) planCompletionFileImports() error {
	if err := p.planSharedFileImports(); err != nil {
		return err
	}
	return p.fileImports.publicSpecs.Require(
		goacodegen.SimpleImport("context"),
		goacodegen.SimpleImport("slices"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/completion"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/model"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/rawjson"),
	)
}

// planSharedFileImports records the fixed imports used by codec, union, and
// HTTP validation files. Optional imports are recorded only when that package
// emits the matching source branch.
func (p *toolSpecsPackagePlan) planSharedFileImports() error {
	codecImports := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("bytes"),
		goacodegen.SimpleImport("encoding/json"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("io"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/tools"),
	}
	if p.hasJSONValidatorKind(jsonValidatorObject, jsonValidatorMap) {
		codecImports = append(codecImports, goacodegen.SimpleImport("sort"))
	}
	if p.hasIndexedJSONValidator() {
		codecImports = append(codecImports, goacodegen.SimpleImport("strconv"))
	}
	hasTransport := false
	hasValidation := false
	hasFieldJSONTypes := false
	for key, planned := range p.types {
		if len(buildFieldJSONTypes(planned.transportShape)) > 0 {
			hasFieldJSONTypes = true
		}
		if key.OwnerKind != contractTypeOwnerTool && !planned.publicLayout.ReferenceIsPointer() {
			continue
		}
		hasTransport = true
		validationShapes := make([]*goaexpr.AttributeExpr, 0, len(planned.transportTypes)+1)
		validationShapes = append(validationShapes, planned.transportShape)
		for _, localized := range planned.transportTypes {
			validationShapes = append(validationShapes, localized.generated.AttributeExpr)
		}
		for _, shape := range validationShapes {
			for _, preference := range goacodegen.ValidationRuntimeImports(
				shape,
				transportValidationLayoutPolicy(shape),
			) {
				if err := p.fileImports.transportValidate.Require(
					goacodegen.NewImport(preference.Name, preference.Path),
				); err != nil {
					return err
				}
				if preference.Path == goacodegen.GoaImport("").Path {
					hasValidation = true
				}
			}
		}
	}
	if hasTransport {
		codecImports = append(codecImports, goacodegen.NewImport("toolhttp", p.transport.ImportPath()))
	}
	if hasValidation {
		codecImports = append(codecImports, goacodegen.GoaImport(""))
	}
	if hasValidation || hasFieldJSONTypes {
		codecImports = append(codecImports, goacodegen.SimpleImport("errors"))
	}
	codecImports = append(codecImports, goacodegen.SimpleImport("strings"))
	if err := p.fileImports.publicCodecs.Require(codecImports...); err != nil {
		return err
	}
	unionImports := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("bytes"),
		goacodegen.SimpleImport("encoding/json"),
		goacodegen.SimpleImport("errors"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("io"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/tools"),
	}
	if len(p.publicUnionErrors) > 0 {
		if err := p.fileImports.publicUnions.Require(unionImports...); err != nil {
			return err
		}
	}
	if len(p.transportUnionErrors) > 0 {
		if err := p.fileImports.transportUnions.Require(unionImports...); err != nil {
			return err
		}
	}
	return nil
}

// hasJSONValidatorKind reports whether any generated raw JSON validator has
// one of the supplied shapes.
func (p *toolSpecsPackagePlan) hasJSONValidatorKind(kinds ...string) bool {
	for _, validator := range p.jsonValidators {
		if slices.Contains(kinds, validator.kind) {
			return true
		}
	}
	return false
}

// hasIndexedJSONValidator reports whether generated source writes an array
// index or parses an integer value.
func (p *toolSpecsPackagePlan) hasIndexedJSONValidator() bool {
	for _, validator := range p.jsonValidators {
		if validator.kind == jsonValidatorArray || validator.signedInteger || validator.unsignedInteger {
			return true
		}
	}
	return false
}

// transportValidationLayoutPolicy matches the pointer rules used when the HTTP
// validator for attribute is written. Primitive values stay values while
// objects, unions, and collection entries preserve null until validation.
func transportValidationLayoutPolicy(attribute *goaexpr.AttributeExpr) goacodegen.GoLayoutPolicy {
	return goacodegen.GoLayoutPolicy{
		Pointer:             !goaexpr.IsPrimitive(attribute.Type),
		UnionPointer:        true,
		ArrayElementPointer: true,
		SumType:             true,
	}
}

// declareToolPackageNames records names shared by all files in one tool package.
func (p *toolSpecsPackagePlan) declareToolPackageNames() error {
	return declareExactNames(p.public, p.publicFixed, map[goacodegen.PackageNameKind][]string{
		goacodegen.NameVariable: {"metadata", "names"},
		goacodegen.NameFunction: {
			"Specs", "Names", "Spec", "PayloadSchema", "ResultSchema", "Metadata", "MetadataByName",
			"RequiredLabels", "PayloadCodec", "ResultCodec", "cloneStringMap", "newValidationError",
			"generatedJSONChildPath", "dottedJSONPathPointer", "escapeJSONPointerToken", "decodedJSONType",
			"generatedUnmarshalJSONType", "SchemaFingerprint", "RegistrationToken",
			"invalidGeneratedFieldTypeError", "unknownJSONFieldError",
		},
	})
}

// declareCompletionPackageNames records names shared by all files in one
// completion package.
func (p *toolSpecsPackagePlan) declareCompletionPackageNames() error {
	return declareExactNames(p.public, p.publicFixed, map[goacodegen.PackageNameKind][]string{
		goacodegen.NameFunction: {
			"newValidationError", "generatedJSONChildPath", "dottedJSONPathPointer",
			"escapeJSONPointerToken", "decodedJSONType", "generatedUnmarshalJSONType",
			"invalidGeneratedFieldTypeError",
			"unknownJSONFieldError",
		},
	})
}

// declareExactNames records names that are written the same way in every file.
func declareExactNames(pkg *goacodegen.GeneratedPackage, records map[string]*goacodegen.NameDeclaration, names map[goacodegen.PackageNameKind][]string) error {
	for kind, values := range names {
		for _, name := range values {
			if records[name] != nil {
				continue
			}
			declaration := goacodegen.NewExactName(kind, name)
			if err := pkg.DeclareName(declaration); err != nil {
				return err
			}
			records[name] = declaration
		}
	}
	return nil
}

// declareToolNames records every constant, variable, and function written for
// one tool.
func (p *toolSpecsPackagePlan) declareToolNames(toolset string, tool *agent.ToolExpr) error {
	qualified := toolset + "." + tool.Name
	constant := goacodegen.NewPreferredName(
		goacodegen.NameConstant,
		tool.Name,
		goacodegen.ExportedName,
		specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":constant"},
	)
	if err := p.public.DeclareName(constant); err != nil {
		return err
	}
	names := &plannedToolNames{
		constant:                 constant,
		serverDataTransforms:     make(map[string]*goacodegen.NameDeclaration),
		serverDataTypes:          make(map[string]*plannedSpecType),
		serverDataTransformPlans: make(map[string]*goacodegen.TransformPlan),
		injectedFieldLayouts:     make(map[string]*goacodegen.GoTypePlan),
	}
	var err error
	names.constructor, err = p.public.DeclareDependentName(
		goacodegen.NameFunction,
		constant,
		"newSpec",
		"",
		specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":constructor"},
	)
	if err != nil {
		return err
	}
	names.spec, err = p.public.DeclareDependentName(
		goacodegen.NameFunction,
		constant,
		"Spec",
		"",
		specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":spec"},
	)
	if err != nil {
		return err
	}
	result := tool.Return
	if (result == nil || result.Type == nil || result.Type == goaexpr.Empty) && tool.Method != nil {
		result = tool.Method.Result
	}
	names.typed, err = p.public.DeclareDependentName(
		goacodegen.NameVariable,
		constant,
		"",
		"Tool",
		specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":typed"},
	)
	if err != nil {
		return err
	}
	if len(tool.InjectedFields) > 0 {
		names.inject, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"Inject",
			"",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":inject"},
		)
		if err != nil {
			return err
		}
		names.decode, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"Decode",
			"",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":decode"},
		)
		if err != nil {
			return err
		}
	}
	if len(tool.ServerData) > 0 {
		names.canonicalizeServerData, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"canonicalize",
			"ServerData",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":server-data"},
		)
		if err != nil {
			return err
		}
		names.canonicalizeServerDataItem, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"canonicalize",
			"ServerDataItem",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":server-data-item"},
		)
		if err != nil {
			return err
		}
	}
	if tool.Method != nil {
		names.methodPayloadTransform, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"Init",
			"MethodPayload",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":method-payload"},
		)
		if err != nil {
			return err
		}
		if result != nil && result.Type != nil && result.Type != goaexpr.Empty {
			names.toolResultTransform, err = p.public.DeclareDependentName(
				goacodegen.NameFunction,
				constant,
				"Init",
				"ToolResult",
				specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":tool-result"},
			)
			if err != nil {
				return err
			}
		}
		for _, serverData := range tool.ServerData {
			if serverData.Source == nil || serverData.Source.MethodResultField == "" {
				continue
			}
			declaration, err := p.public.DeclareDependentName(
				goacodegen.NameFunction,
				constant,
				"Init",
				goacodegen.Goify(serverData.Kind, true)+"ServerData",
				specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":server-data-transform:" + serverData.Kind},
			)
			if err != nil {
				return err
			}
			names.serverDataTransforms[serverData.Kind] = declaration
		}
	}
	if tool.Method != nil && tool.Bounds != nil {
		names.bounds, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"init",
			"Bounds",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":bounds"},
		)
		if err != nil {
			return err
		}
	}
	p.tools[tool.Name] = names
	return nil
}

// declareCompletionNames records every name written for one completion.
func (p *toolSpecsPackagePlan) declareCompletionNames(name string) error {
	constant := goacodegen.NewPreferredName(
		goacodegen.NameConstant,
		name,
		goacodegen.ExportedName,
		specNameOrder{packagePath: p.public.ImportPath(), key: "completion:" + name + ":constant"},
	)
	if err := p.public.DeclareName(constant); err != nil {
		return err
	}
	names := &plannedCompletionNames{constant: constant}
	declare := func(prefix, suffix, role string) (*goacodegen.NameDeclaration, error) {
		return p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			prefix,
			suffix,
			specNameOrder{packagePath: p.public.ImportPath(), key: "completion:" + name + ":" + role},
		)
	}
	var err error
	if names.spec, err = declare("spec", "", "spec"); err != nil {
		return err
	}
	if names.example, err = declare("", "Example", "example"); err != nil {
		return err
	}
	if names.complete, err = declare("Complete", "", "complete"); err != nil {
		return err
	}
	if names.streamComplete, err = declare("StreamComplete", "", "stream-complete"); err != nil {
		return err
	}
	p.completionNames[name] = names
	return nil
}

// packageFor returns the saved public and HTTP package settings for dir.
func (p *toolSpecsPlan) packageFor(dir string) (*toolSpecsPackagePlan, error) {
	planned := p.byDir[dir]
	if planned == nil {
		return nil, fmt.Errorf("tool specs package %q was not planned", dir)
	}
	return planned, nil
}

// link builds each tool package once after Goa has built the service types.
// Every reference to the same output directory reads the same saved data.
func (p *toolSpecsPlan) link(data *GeneratorData) error {
	services := p.service.Services()
	dirs := make([]string, 0, len(p.byDir))
	for dir := range p.byDir {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	for _, dir := range dirs {
		planned := p.byDir[dir]
		if err := planned.fileImports.link(); err != nil {
			return fmt.Errorf("link tool specs package %q imports: %w", dir, err)
		}
		owner, err := newDefinitionToolsetData(planned.definition, services, p.mcp)
		if err != nil {
			return err
		}
		if planned.registry {
			planned.render = owner
			continue
		}
		if err := linkToolsetNames(planned, owner); err != nil {
			return err
		}
		if err := p.linkMethodToolset(planned, owner, services); err != nil {
			return err
		}
		planned.specs, err = buildToolSpecsDataForPackage(
			data.Genpkg,
			owner.SourceService,
			owner.Tools,
			planned,
			p.api,
		)
		if err != nil {
			return err
		}
		if toolsetHasMethodTools(owner) {
			planned.specs.providerImports = importsForPaths(planned.public, planned.providerImportPaths)
			planned.specs.serviceTypeRef = planned.public.ImportName(planned.serviceImportPath) + "." + owner.SourceService.ServiceDeclaration.Name()
		}
		if err := planned.linkToolTransforms(owner, services); err != nil {
			return err
		}
		owner.specs = planned.specs
		planned.render = owner
	}

	for _, service := range data.Services {
		for _, agent := range service.Agents {
			for _, toolset := range agent.AllToolsets {
				if toolset == nil || len(toolset.Tools) == 0 {
					continue
				}
				planned, err := p.packageFor(toolset.SpecsDir)
				if err != nil {
					return err
				}
				if err := linkToolsetNames(planned, toolset); err != nil {
					return err
				}
				if err := p.linkMethodToolset(planned, toolset, services); err != nil {
					return err
				}
				toolset.specs = planned.specs
			}
		}
		if len(service.Completions) == 0 {
			continue
		}
		planned := p.completions[service.Service.Name]
		if planned == nil {
			return fmt.Errorf("service %q completion package was not planned", service.Service.Name)
		}
		if err := planned.fileImports.link(); err != nil {
			return fmt.Errorf("link service %q completion imports: %w", service.Service.Name, err)
		}
		var err error
		planned.completion, err = buildCompletionSpecsDataForPackage(
			data.Genpkg,
			service.Service,
			service.Completions,
			planned,
			p.api,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// declareToolTypeImports records every package needed by the input, result,
// and server data before generated declarations request names in the same Go
// package.
func (p *toolSpecsPackagePlan) declareToolTypeImports(toolset string, tool *agent.ToolExpr) error {
	owner := &contractTypeOwner{
		Kind:                     contractTypeOwnerTool,
		Name:                     tool.Name,
		QualifiedName:            toolset + "." + tool.Name,
		ScopeName:                toolset,
		Bounds:                   boundsData(tool.Bounds, tool.Method),
		ModelHiddenPayloadFields: slices.Clone(tool.InjectedFields),
	}
	payload := tool.Args
	if payload == nil || payload.Type == nil || payload.Type == goaexpr.Empty {
		payload = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}
	if err := p.declareTypeImports(owner, payload, usagePayload); err != nil {
		return err
	}
	result := tool.Return
	if (result == nil || result.Type == nil || result.Type == goaexpr.Empty) && tool.Method != nil {
		result = tool.Method.Result
	}
	if result == nil {
		result = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
	if err := p.declareTypeImports(owner, result, usageResult); err != nil {
		return err
	}
	for _, serverData := range tool.ServerData {
		if serverData == nil || serverData.Schema == nil {
			continue
		}
		if err := p.declareTypeImports(owner, serverData.Schema, usageServerData); err != nil {
			return err
		}
	}
	return nil
}

// declareToolTypes records the input, result, and server-data types generated
// for tool.
func (p *toolSpecsPackagePlan) declareToolTypes(toolset string, tool *agent.ToolExpr) error {
	owner := &contractTypeOwner{
		Kind:                     contractTypeOwnerTool,
		Name:                     tool.Name,
		QualifiedName:            toolset + "." + tool.Name,
		ScopeName:                toolset,
		Bounds:                   boundsData(tool.Bounds, tool.Method),
		ModelHiddenPayloadFields: slices.Clone(tool.InjectedFields),
	}
	payload := tool.Args
	if payload == nil || payload.Type == nil || payload.Type == goaexpr.Empty {
		payload = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}
	if err := p.declareType(owner, payload, usagePayload, ""); err != nil {
		return err
	}
	names := p.tools[tool.Name]
	names.payloadType = p.types[stableTypeKey(owner, usagePayload, "")]
	names.payloadType.jsonValidator.render = true
	publicPayload := effectiveObject(names.payloadType.publicShape)
	for _, name := range tool.InjectedFields {
		field := publicPayload.Attribute(name)
		if field == nil {
			return fmt.Errorf("plan injected field %q for tool %q: field is missing", name, tool.Name)
		}
		layout, err := goacodegen.PlanGoType(field, goacodegen.GoTypePlanOptions{
			Owner:     p.public.ImportPath(),
			FieldName: name,
			Policy: goacodegen.GoLayoutPolicy{
				UseDefault: true,
				SumType:    true,
			},
		})
		if err != nil {
			return fmt.Errorf("plan injected field %q for tool %q: %w", name, tool.Name, err)
		}
		names.injectedFieldLayouts[name] = layout
	}
	result := tool.Return
	if (result == nil || result.Type == nil || result.Type == goaexpr.Empty) && tool.Method != nil {
		result = tool.Method.Result
	}
	if result == nil {
		result = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
	if err := p.declareType(owner, result, usageResult, ""); err != nil {
		return err
	}
	names.resultType = p.types[stableTypeKey(owner, usageResult, "")]
	if result.Type != goaexpr.Empty {
		names.resultType.jsonValidator.render = true
	}
	for _, serverData := range tool.ServerData {
		if serverData == nil || serverData.Schema == nil {
			continue
		}
		if err := p.declareType(owner, serverData.Schema, usageServerData, serverData.Kind); err != nil {
			return err
		}
		names.serverDataTypes[serverData.Kind] = p.types[stableTypeKey(owner, usageServerData, serverData.Kind)]
		names.serverDataTypes[serverData.Kind].jsonValidator.render = true
	}
	return nil
}

// declareToolTransforms records every conversion used by a method-backed tool.
func (p *toolSpecsPackagePlan) declareToolTransforms(toolset string, tool *agent.ToolExpr) error {
	if tool.Method == nil {
		return nil
	}
	qualified := toolset + "." + tool.Name
	names := p.tools[tool.Name]
	owner := &contractTypeOwner{
		Kind:          contractTypeOwnerTool,
		Name:          tool.Name,
		QualifiedName: qualified,
		ScopeName:     toolset,
	}
	payload := p.types[stableTypeKey(owner, usagePayload, "")]
	if payload != nil && tool.Method.Payload != nil && tool.Method.Payload.Type != goaexpr.Empty {
		if err := goacodegen.IsCompatible(payload.public.Type, tool.Method.Payload.Type, "in", "out"); err == nil {
			planned, err := p.declareAdapterTransform(qualified+":method-payload", payload.public, tool.Method.Payload)
			if err != nil {
				return err
			}
			names.methodPayloadTransformPlan = planned
		}
	}
	result := p.types[stableTypeKey(owner, usageResult, "")]
	if result != nil && tool.Method.Result != nil && tool.Method.Result.Type != goaexpr.Empty {
		if err := goacodegen.IsCompatible(tool.Method.Result.Type, result.public.Type, "in", "out"); err == nil {
			planned, err := p.declareAdapterTransform(qualified+":tool-result", tool.Method.Result, result.public)
			if err != nil {
				return err
			}
			names.toolResultTransformPlan = planned
		}
	}
	for _, serverData := range tool.ServerData {
		if serverData.Source == nil || serverData.Source.MethodResultField == "" {
			continue
		}
		source := tool.Method.Result.Find(serverData.Source.MethodResultField)
		target := p.types[stableTypeKey(owner, usageServerData, serverData.Kind)]
		if source == nil || target == nil {
			return fmt.Errorf("server data kind %q has no source or output type", serverData.Kind)
		}
		if err := goacodegen.IsCompatible(source.Type, target.public.Type, "in", "out"); err != nil {
			return fmt.Errorf("server data kind %q: %w", serverData.Kind, err)
		}
		planned, err := p.declareAdapterTransform(qualified+":server-data:"+serverData.Kind, source, target.public)
		if err != nil {
			return err
		}
		names.serverDataTransformPlans[serverData.Kind] = planned
	}
	return nil
}

// declareType records one public Go type, its JSON-decoding Go type, every
// union it contains, and the functions that copy values between the two types.
func (p *toolSpecsPackagePlan) declareType(owner *contractTypeOwner, attribute *goaexpr.AttributeExpr, usage typeUsage, qualifier string) error {
	identity := stableTypeKey(owner, usage, qualifier)
	if p.types[identity] != nil {
		return nil
	}
	key := identity.orderKey()
	preferred := goacodegen.Goify(owner.Name, true)
	switch usage {
	case usagePayload:
		preferred += "Payload"
	case usageResult:
		preferred += "Result"
	case usageServerData:
		preferred += goacodegen.Goify(qualifier, true) + "ServerData"
	}
	shapes := localizedSpecShapes(owner, attribute, usage)
	public := shapes.public
	publicTypes := shapes.publicTypes
	if err := p.declareLocalTypes(p.public, p.publicTypes, p.publicTypeUses, publicTypes); err != nil {
		return err
	}
	publicType := &goaexpr.UserTypeExpr{
		AttributeExpr: stripStructPkgMeta(public),
		TypeName:      preferred,
	}
	publicAttribute := &goaexpr.AttributeExpr{Type: publicType}
	publicTypeDeclaration, err := p.public.DeclareGeneratedType(
		preferred,
		specNameOrder{packagePath: p.public.ImportPath(), key: key + ":public"},
	)
	if err != nil {
		return err
	}
	if err := p.public.BindGeneratedType(publicType, publicTypeDeclaration); err != nil {
		return err
	}
	publicDeclaration := publicTypeDeclaration.Declaration()
	p.publicTypeUses[publicType] = publicDeclaration
	if err := declareAttributeUnions(p.public, p.publicFixed, p.publicUnionErrors, public); err != nil {
		return err
	}

	publicLayout, err := p.planDeclaredTypeLayout(
		publicAttribute,
		p.public,
		goacodegen.GoLayoutPolicy{UseDefault: true, SumType: true},
	)
	if err != nil {
		return err
	}

	var (
		transportAttribute   *goaexpr.AttributeExpr
		transportDeclaration *goacodegen.NameDeclaration
		transportLayout      *goacodegen.GoTypePlan
		decode, encode       *goacodegen.TransformPlan
	)
	if shapes.usesTransport {
		if err := p.declareLocalTypes(p.transport, p.transportTypes, p.transportTypeUses, shapes.transportTypes); err != nil {
			return err
		}
		transportType := &goaexpr.UserTypeExpr{
			AttributeExpr: shapes.transport,
			TypeName:      preferred + "Transport",
		}
		transportAttribute = &goaexpr.AttributeExpr{Type: transportType}
		transportTypeDeclaration, declareErr := p.transport.DeclareGeneratedType(
			transportType.TypeName,
			specNameOrder{packagePath: p.transport.ImportPath(), key: key + ":transport"},
		)
		if declareErr != nil {
			return declareErr
		}
		if err := p.transport.BindGeneratedType(transportType, transportTypeDeclaration); err != nil {
			return err
		}
		transportDeclaration = transportTypeDeclaration.Declaration()
		p.transportTypeUses[transportType] = transportDeclaration
		if err := declareAttributeUnions(p.transport, p.transportFixed, p.transportUnionErrors, shapes.transport); err != nil {
			return err
		}
		transportLayout, err = p.planDeclaredTypeLayout(
			transportAttribute,
			p.transport,
			goacodegen.GoLayoutPolicy{
				Pointer:             true,
				UnionPointer:        true,
				ArrayElementPointer: true,
				SumType:             true,
			},
		)
		if err != nil {
			return err
		}
		decode, err = p.declareTransform(key+":decode", transportAttribute, publicAttribute, "decode")
		if err != nil {
			return err
		}
		encode, err = p.declareTransform(key+":encode", publicAttribute, transportAttribute, "encode")
		if err != nil {
			return err
		}
	}
	names, err := p.declareTypeNames(
		key,
		preferred,
		owner.Kind == contractTypeOwnerCompletion,
		publicDeclaration,
		transportDeclaration,
	)
	if err != nil {
		return err
	}
	jsonValidator, err := p.declareJSONValidator(key, preferred, cloneModelSchemaAttribute(shapes.transport), owner, usage)
	if err != nil {
		return err
	}
	p.types[identity] = &plannedSpecType{
		publicDeclaration:    publicDeclaration,
		transportDeclaration: transportDeclaration,
		publicLayout:         publicLayout,
		transportLayout:      transportLayout,
		publicShape:          public,
		transportShape:       shapes.transport,
		publicTypes:          publicTypes,
		transportTypes:       shapes.transportTypes,
		public:               publicAttribute,
		transport:            transportAttribute,
		decode:               decode,
		encode:               encode,
		exportedCodec:        names.exportedCodec,
		genericCodec:         names.genericCodec,
		marshal:              names.marshal,
		unmarshal:            names.unmarshal,
		transportValidator:   names.transportValidator,
		fieldDescriptions:    names.fieldDescriptions,
		fieldJSONTypes:       names.fieldJSONTypes,
		enrichValidation:     names.enrichValidation,
		invalidFieldType:     names.invalidFieldType,
		jsonValidator:        jsonValidator,
	}
	if shapes.usesTransport {
		p.setTransformLayouts(decode, transportLayout, publicLayout)
		p.setTransformLayouts(encode, publicLayout, transportLayout)
	}
	return nil
}

// declareTypeImports records the packages written by the generated type,
// codec, union, and validation files before Goa chooses their final names.
func (p *toolSpecsPackagePlan) declareTypeImports(owner *contractTypeOwner, attribute *goaexpr.AttributeExpr, usage typeUsage) error {
	shapes := localizedSpecShapes(owner, attribute, usage)
	for _, localized := range shapes.publicTypes {
		if err := p.fileImports.publicTypes.AddTypeExpressions(localized.generated.AttributeExpr); err != nil {
			return err
		}
	}
	if err := p.fileImports.publicTypes.AddTypeExpressions(shapes.public); err != nil {
		return err
	}
	if err := p.fileImports.publicCodecs.AddRecursiveTypeReferences(shapes.public, shapes.transport); err != nil {
		return err
	}
	if err := p.fileImports.publicUnions.AddTypeExpressions(shapes.public); err != nil {
		return err
	}
	if !shapes.usesTransport {
		return nil
	}
	for _, localized := range shapes.transportTypes {
		if err := p.fileImports.transportTypes.AddTypeExpressions(localized.generated.AttributeExpr); err != nil {
			return err
		}
	}
	if err := p.fileImports.transportTypes.AddTypeExpressions(shapes.transport); err != nil {
		return err
	}
	return p.fileImports.transportUnions.AddTypeExpressions(shapes.transport)
}

// localizedSpecShapes copies one design type into the two package-specific
// forms emitted by Goa-AI. Import planning and declaration use this function,
// so a nested type cannot appear only after package names are fixed.
func localizedSpecShapes(owner *contractTypeOwner, attribute *goaexpr.AttributeExpr, usage typeUsage) localizedSpecTypeShapes {
	shape := attribute
	if userType, ok := attribute.Type.(goaexpr.UserType); ok && userType != goaexpr.Empty {
		shape = userType.Attribute()
	}
	public, publicTypes := localizeNestedTypes(shape, false, nil)
	transportSource := public
	if usage == usagePayload && len(owner.ModelHiddenPayloadFields) > 0 {
		transportSource = modelTransportShape(public, owner.ModelHiddenPayloadFields)
	}
	transport := cloneWithModelJSONTags(transportSource)
	publicSources := make(map[goaexpr.UserType]goaexpr.UserType, len(publicTypes))
	for _, localized := range publicTypes {
		publicSources[localized.generated] = localized.source
	}
	transport, transportTypes := localizeNestedTypes(transport, true, publicSources)
	return localizedSpecTypeShapes{
		public:         public,
		publicTypes:    publicTypes,
		transport:      transport,
		transportTypes: transportTypes,
		usesTransport:  owner.Kind == contractTypeOwnerTool || goaexpr.IsObject(public.Type) || goaexpr.IsUnion(public.Type),
	}
}

// planDeclaredTypeLayout asks Goa how a generated named type is referenced.
// Later generator steps use the saved answer instead of inspecting rendered Go
// source for pointer characters.
func (p *toolSpecsPackagePlan) planDeclaredTypeLayout(
	attribute *goaexpr.AttributeExpr,
	pkg *goacodegen.GeneratedPackage,
	policy goacodegen.GoLayoutPolicy,
) (*goacodegen.GoTypePlan, error) {
	return goacodegen.PlanGoType(attribute, goacodegen.GoTypePlanOptions{
		Owner:            pkg.ImportPath(),
		Policy:           policy,
		RetainNamedValue: true,
		Bind: func(request goacodegen.GoTypeBindingRequest) (goacodegen.GoTypeBinding, error) {
			owner := request.InheritedOwner
			if location := goacodegen.UserTypeLocation(request.Attribute.Type); location != nil {
				owner = path.Join(p.genpkg, location.RelImportPath)
			}
			generated := p.generation.Package(owner)
			switch request.Kind {
			case goacodegen.GoNamed:
				declaration, err := generated.Type(request.Attribute.Type.(goaexpr.UserType))
				if err != nil {
					return goacodegen.GoTypeBinding{}, err
				}
				return goacodegen.GoTypeBinding{Owner: owner, Type: declaration}, nil
			case goacodegen.GoUnion:
				declaration, err := generated.Union(request.Attribute)
				if err != nil {
					return goacodegen.GoTypeBinding{}, err
				}
				return goacodegen.GoTypeBinding{Owner: owner, Union: declaration}, nil
			case goacodegen.GoPrimitive,
				goacodegen.GoArray,
				goacodegen.GoMap,
				goacodegen.GoStruct,
				goacodegen.GoEmpty,
				goacodegen.GoServiceError:
				return goacodegen.GoTypeBinding{}, fmt.Errorf("generated type layout requested unsupported %s", request.Kind)
			}
			panic(fmt.Sprintf("unknown generated type layout kind %d", request.Kind))
		},
	})
}

// declareTypeNames records the variables and functions written with one type.
func (p *toolSpecsPackagePlan) declareTypeNames(key, preferred string, completion bool, public, transport *goacodegen.NameDeclaration) (*plannedSpecType, error) {
	names := &plannedSpecType{}
	declarePublic := func(prefix, suffix, role string) (*goacodegen.NameDeclaration, error) {
		return p.public.DeclareDependentName(
			goacodegen.NameFunction,
			public,
			prefix,
			suffix,
			specNameOrder{packagePath: p.public.ImportPath(), key: key + ":" + role},
		)
	}
	declarePrivate := func(kind goacodegen.PackageNameKind, name, role string) (*goacodegen.NameDeclaration, error) {
		declaration := goacodegen.NewPreferredName(
			kind,
			name,
			goacodegen.UnexportedName,
			specNameOrder{packagePath: p.public.ImportPath(), key: key + ":" + role},
		)
		if err := p.public.DeclareName(declaration); err != nil {
			return nil, err
		}
		return declaration, nil
	}
	var err error
	codecPrefix := ""
	if completion {
		codecPrefix = "new"
	}
	names.exportedCodec, err = declarePublic(codecPrefix, "Codec", "codec")
	if err != nil {
		return nil, err
	}
	names.genericCodec, err = declarePrivate(goacodegen.NameVariable, lowerCamel(preferred)+"Codec", "generic-codec")
	if err != nil {
		return nil, err
	}
	marshalPrefix := "Marshal"
	unmarshalPrefix := "Unmarshal"
	if completion {
		marshalPrefix = "marshal"
		unmarshalPrefix = "unmarshal"
	}
	names.marshal, err = declarePublic(marshalPrefix, "", "marshal")
	if err != nil {
		return nil, err
	}
	names.unmarshal, err = declarePublic(unmarshalPrefix, "", "unmarshal")
	if err != nil {
		return nil, err
	}
	names.fieldDescriptions, err = declarePrivate(
		goacodegen.NameVariable,
		lowerCamel(preferred)+"FieldDescs",
		"field-descriptions",
	)
	if err != nil {
		return nil, err
	}
	names.fieldJSONTypes, err = declarePrivate(
		goacodegen.NameVariable,
		lowerCamel(preferred)+"FieldJSONTypes",
		"field-json-types",
	)
	if err != nil {
		return nil, err
	}
	names.enrichValidation, err = declarePublic("enrich", "ValidationError", "enrich-validation")
	if err != nil {
		return nil, err
	}
	names.invalidFieldType, err = declarePublic("invalid", "FieldTypeError", "invalid-field-type")
	if err != nil {
		return nil, err
	}
	if transport != nil {
		names.transportValidator, err = p.transport.DeclareDependentName(
			goacodegen.NameFunction,
			transport,
			"Validate",
			"",
			specNameOrder{packagePath: p.transport.ImportPath(), key: key + ":validate"},
		)
		if err != nil {
			return nil, err
		}
	}
	return names, nil
}

// declareLocalTypes records the nested types that one output package writes.
func (p *toolSpecsPackagePlan) declareLocalTypes(pkg *goacodegen.GeneratedPackage, declared map[goaexpr.UserType]*goacodegen.TypeDeclaration, uses map[goaexpr.UserType]*goacodegen.NameDeclaration, types []*localizedType) error {
	for _, localized := range types {
		if declaration := declared[localized.source]; declaration != nil {
			if err := pkg.BindGeneratedType(localized.generated, declaration); err != nil {
				return err
			}
			localized.declaration = declaration
			uses[localized.generated] = declaration.Declaration()
			continue
		}
		declaration, err := pkg.DeclareGeneratedType(
			localized.generated.TypeName,
			newLocalizedTypeNameOrder(pkg.ImportPath(), localized.source, localizedTypeDeclarationName),
		)
		if err != nil {
			return err
		}
		if err := pkg.BindGeneratedType(localized.generated, declaration); err != nil {
			return err
		}
		declared[localized.source] = declaration
		localized.declaration = declaration
		uses[localized.generated] = declaration.Declaration()
		if pkg == p.transport {
			validator, err := pkg.DeclareDependentName(
				goacodegen.NameFunction,
				declaration.Declaration(),
				"Validate",
				"",
				newLocalizedTypeNameOrder(pkg.ImportPath(), localized.source, localizedTypeValidatorName),
			)
			if err != nil {
				return err
			}
			p.transportValidators[localized.source] = validator
		}
	}
	return nil
}

// declareTransform records the helper functions needed to copy one generated
// type into another and saves each chosen name with its function.
func (p *toolSpecsPackagePlan) declareTransform(key string, source, target *goaexpr.AttributeExpr, prefix string) (*goacodegen.TransformPlan, error) {
	planned, err := p.recordTransform(key, source, target, prefix)
	if err != nil {
		return nil, err
	}
	return planned.plan, nil
}

// declareAdapterTransform records one conversion written to transforms.go.
func (p *toolSpecsPackagePlan) declareAdapterTransform(key string, source, target *goaexpr.AttributeExpr) (*goacodegen.TransformPlan, error) {
	planned, err := p.recordTransform(key, source, target, "")
	if err != nil {
		return nil, err
	}
	p.adapterTransformPlans = append(p.adapterTransformPlans, planned)
	return planned.plan, nil
}

// recordTransform saves one conversion for package-wide helper planning.
func (p *toolSpecsPackagePlan) recordTransform(key string, source, target *goaexpr.AttributeExpr, prefix string) (*plannedPackageTransform, error) {
	planned, err := goacodegen.NewTransformPlan(source, target, prefix, nil)
	if err != nil {
		return nil, err
	}
	transform := &plannedPackageTransform{
		key:    key,
		prefix: prefix,
		plan:   planned,
	}
	p.transformPlans = append(p.transformPlans, transform)
	return transform, nil
}

// declareAttributeUnions records every union reachable from attribute once.
func declareAttributeUnions(pkg *goacodegen.GeneratedPackage, fixed map[string]*goacodegen.NameDeclaration, helpers map[goacodegen.UnionDeclarationID]*goacodegen.NameDeclaration, attribute *goaexpr.AttributeExpr) error {
	seenTypes := make(map[goaexpr.UserType]struct{})
	seenUnions := make(map[goacodegen.UnionDeclarationID]struct{})
	var visit func(*goaexpr.AttributeExpr) error
	visit = func(current *goaexpr.AttributeExpr) error {
		if current == nil || current.Type == nil || current.Type == goaexpr.Empty {
			return nil
		}
		switch actual := current.Type.(type) {
		case goaexpr.UserType:
			if goacodegen.UserTypeLocation(actual) != nil {
				return nil
			}
			origin := actual.Origin()
			if _, ok := seenTypes[origin]; ok {
				return nil
			}
			seenTypes[origin] = struct{}{}
			return visit(actual.Attribute())
		case *goaexpr.Object:
			for _, field := range *actual {
				if err := visit(field.Attribute); err != nil {
					return err
				}
			}
		case *goaexpr.Array:
			return visit(actual.ElemType)
		case *goaexpr.Map:
			if err := visit(actual.KeyType); err != nil {
				return err
			}
			return visit(actual.ElemType)
		case *goaexpr.Union:
			if err := declareExactNames(pkg, fixed, map[goacodegen.PackageNameKind][]string{
				goacodegen.NameFunction: {"decodeUnionStrictJSON", "missingUnionValueError", "nullUnionValueError"},
			}); err != nil {
				return err
			}
			identity := goacodegen.NewUnionDeclarationID(current)
			if _, ok := seenUnions[identity]; ok {
				return nil
			}
			seenUnions[identity] = struct{}{}
			declaration, err := pkg.DeclareUnion(current)
			if err != nil {
				return err
			}
			if helpers[identity] == nil {
				helper, err := pkg.DeclareDependentName(
					goacodegen.NameFunction,
					declaration.Declaration(),
					"new",
					"DiscriminatorError",
					unionErrorNameOrder{packagePath: pkg.ImportPath(), unionName: actual.Name()},
				)
				if err != nil {
					return err
				}
				helpers[identity] = helper
			}
			for _, branch := range actual.Values {
				if err := visit(branch.Attribute); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(attribute)
}

// ComparePackageName orders two generated tool specification names.
func (o specNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(specNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.key, right.key)
}

// ComparePackageName orders conversion helpers by their owning transform and
// authored location.
func (o transformHelperNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(transformHelperNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	if compared := strings.Compare(o.key, right.key); compared != 0 {
		return compared
	}
	return o.location.Compare(right.location)
}

// ComparePackageName orders unknown-branch error functions by package and the
// exact union name that owns each function.
func (o unionErrorNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(unionErrorNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.unionName, right.unionName)
}

// ComparePackageName orders generated nested types by their source package,
// name, and ID.
func (o localizedTypeNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(localizedTypeNameOrder)
	for _, compared := range []int{
		strings.Compare(o.packagePath, right.packagePath),
		strings.Compare(o.sourcePath, right.sourcePath),
		strings.Compare(o.sourceName, right.sourceName),
		strings.Compare(o.sourceID, right.sourceID),
		int(o.role) - int(right.role),
	} {
		if compared != 0 {
			return compared
		}
	}
	return 0
}

// newLocalizedTypeNameOrder copies the stable source type details used while
// Goa assigns generated package names.
func newLocalizedTypeNameOrder(packagePath string, source goaexpr.UserType, role localizedTypeNameRole) localizedTypeNameOrder {
	var sourcePath string
	if location := goacodegen.UserTypeLocation(source); location != nil {
		sourcePath = location.RelImportPath
	}
	return localizedTypeNameOrder{
		packagePath: packagePath,
		sourcePath:  sourcePath,
		sourceName:  source.Name(),
		sourceID:    source.ID(),
		role:        role,
	}
}
