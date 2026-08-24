// This file records every Go name and type conversion used by generated tool
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
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// toolSpecsPlan stores the public and HTTP packages for every toolset and
	// completion service, plus the Goa API and MCP design used to build them.
	toolSpecsPlan struct {
		byDir       map[string]*toolSpecsPackagePlan
		completions map[string]*toolSpecsPackagePlan
		api         *goaexpr.APIExpr
		mcp         *mcpexpr.RootExpr
	}

	// toolSpecsPackagePlan stores the public specs package and the HTTP helper
	// package written for one toolset.
	toolSpecsPackagePlan struct {
		definition           *ir.Toolset
		public               *goacodegen.GeneratedPackage
		transport            *goacodegen.GeneratedPackage
		types                map[string]*plannedSpecType
		publicTypes          map[string]*goacodegen.NameDeclaration
		transportTypes       map[string]*goacodegen.NameDeclaration
		publicTypeUses       map[goaexpr.UserType]*goacodegen.NameDeclaration
		transportTypeUses    map[goaexpr.UserType]*goacodegen.NameDeclaration
		transportValidators  map[string]*goacodegen.NameDeclaration
		publicFixed          map[string]*goacodegen.NameDeclaration
		transportFixed       map[string]*goacodegen.NameDeclaration
		publicUnionErrors    map[goacodegen.UnionTypeID]*goacodegen.NameDeclaration
		transportUnionErrors map[goacodegen.UnionTypeID]*goacodegen.NameDeclaration
		tools                map[string]*plannedToolNames
		completionNames      map[string]*plannedCompletionNames
		transformHelpers     []*plannedTransformHelper
		specs                *toolSpecsData
		completion           *completionSpecsData
	}

	// plannedSpecType stores one public Go type, its JSON-decoding Go type, and
	// the functions that copy values between them.
	plannedSpecType struct {
		publicDeclaration    *goacodegen.NameDeclaration
		transportDeclaration *goacodegen.NameDeclaration
		publicShape          *goaexpr.AttributeExpr
		transportShape       *goaexpr.AttributeExpr
		publicTypes          []*goaexpr.UserTypeExpr
		transportTypes       []*goaexpr.UserTypeExpr
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
		allowedObjectKeys    *goacodegen.NameDeclaration
		enrichValidation     *goacodegen.NameDeclaration
		invalidFieldType     *goacodegen.NameDeclaration
	}

	// plannedToolNames stores every name written for one tool.
	plannedToolNames struct {
		constant                   *goacodegen.NameDeclaration
		spec                       *goacodegen.NameDeclaration
		typed                      *goacodegen.NameDeclaration
		inject                     *goacodegen.NameDeclaration
		decode                     *goacodegen.NameDeclaration
		canonicalizeServerData     *goacodegen.NameDeclaration
		canonicalizeServerDataItem *goacodegen.NameDeclaration
		methodPayloadTransform     *goacodegen.NameDeclaration
		toolResultTransform        *goacodegen.NameDeclaration
		serverDataTransforms       map[string]*goacodegen.NameDeclaration
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
		decode         *goacodegen.NameDeclaration
		decodeChunk    *goacodegen.NameDeclaration
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
	for _, toolset := range design.Toolsets {
		if toolset == nil || toolset.Expr == nil || len(toolset.Expr.Tools) == 0 {
			continue
		}
		if err := planned.addToolPackage(generation, toolset, toolset.Name, toolset.Name, toolset.SpecsImportPath, toolset.SpecsDir, toolset.Expr.Tools); err != nil {
			return nil, err
		}
	}
	for _, agentDesign := range design.Agents {
		for _, reference := range slices.Concat(agentDesign.UsedToolsets, agentDesign.ExportedToolsets) {
			if reference == nil || reference.Expr == nil || planned.byDir[reference.SpecsDir] != nil {
				continue
			}
			tools, err := toolExpressionsForReference(planned.mcp, reference)
			if err != nil {
				return nil, err
			}
			if len(tools) == 0 {
				continue
			}
			if err := planned.addToolPackage(generation, reference.Definition, reference.Name, reference.QualifiedName, reference.SpecsImportPath, reference.SpecsDir, tools); err != nil {
				return nil, err
			}
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
			packagePlan = newToolSpecsPackagePlan(public, transport)
			if err := packagePlan.declareCompletionPackageNames(); err != nil {
				return nil, fmt.Errorf("plan service %q completion package names: %w", completion.Service.Name, err)
			}
			planned.completions[completion.Service.Name] = packagePlan
		}
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
	}
	return planned, nil
}

// addToolPackage records all names and conversions written to one tool package.
func (p *toolSpecsPlan) addToolPackage(
	generation *goacodegen.Generation,
	definition *ir.Toolset,
	label, qualified, importPath, outputDir string,
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
	packagePlan := newToolSpecsPackagePlan(public, transport)
	packagePlan.definition = definition
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
	p.byDir[outputDir] = packagePlan
	return nil
}

// toolExpressionsForReference returns the tool contracts written for one
// agent's toolset package.
func toolExpressionsForReference(mcpRoot *mcpexpr.RootExpr, reference *ir.ToolsetRef) ([]*agent.ToolExpr, error) {
	if reference.Provider == nil || reference.Provider.Kind != agent.ProviderMCP || reference.Provider.MCP.Source != agent.MCPSourceGoa {
		return reference.Expr.Tools, nil
	}
	var server *mcpexpr.MCPExpr
	if mcpRoot != nil {
		server = mcpRoot.ServiceMCP(reference.Expr.Provider.MCPService, reference.Expr.Provider.MCPToolset)
	}
	if server == nil {
		return nil, fmt.Errorf(
			"toolset %q could not resolve Goa-defined MCP toolset %q on service %q",
			reference.Name,
			reference.Expr.Provider.MCPToolset,
			reference.Expr.Provider.MCPService,
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
func newToolSpecsPackagePlan(public, transport *goacodegen.GeneratedPackage) *toolSpecsPackagePlan {
	return &toolSpecsPackagePlan{
		public:               public,
		transport:            transport,
		types:                make(map[string]*plannedSpecType),
		publicTypes:          make(map[string]*goacodegen.NameDeclaration),
		transportTypes:       make(map[string]*goacodegen.NameDeclaration),
		publicTypeUses:       make(map[goaexpr.UserType]*goacodegen.NameDeclaration),
		transportTypeUses:    make(map[goaexpr.UserType]*goacodegen.NameDeclaration),
		transportValidators:  make(map[string]*goacodegen.NameDeclaration),
		publicFixed:          make(map[string]*goacodegen.NameDeclaration),
		transportFixed:       make(map[string]*goacodegen.NameDeclaration),
		publicUnionErrors:    make(map[goacodegen.UnionTypeID]*goacodegen.NameDeclaration),
		transportUnionErrors: make(map[goacodegen.UnionTypeID]*goacodegen.NameDeclaration),
		tools:                make(map[string]*plannedToolNames),
		completionNames:      make(map[string]*plannedCompletionNames),
	}
}

// declareToolPackageNames records names shared by all files in one tool package.
func (p *toolSpecsPackagePlan) declareToolPackageNames() error {
	return declareExactNames(p.public, p.publicFixed, map[goacodegen.PackageNameKind][]string{
		goacodegen.NameVariable: {"Specs", "metadata", "names", "RequiredLabels"},
		goacodegen.NameFunction: {
			"Names", "Spec", "PayloadSchema", "ResultSchema", "Metadata", "MetadataByName",
			"PayloadCodec", "ResultCodec", "newValidationError", "decodeStrictJSON",
			"decodeKnownJSON", "validateKnownJSONFields", "validateKnownJSONValue",
			"unknownJSONFieldError",
		},
	})
}

// declareCompletionPackageNames records names shared by all files in one
// completion package.
func (p *toolSpecsPackagePlan) declareCompletionPackageNames() error {
	return declareExactNames(p.public, p.publicFixed, map[goacodegen.PackageNameKind][]string{
		goacodegen.NameFunction: {
			"newValidationError", "decodeStrictJSON", "decodeKnownJSON",
			"validateKnownJSONFields", "validateKnownJSONValue", "unknownJSONFieldError",
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
	}
	var err error
	names.spec, err = p.public.DeclareDependentName(
		goacodegen.NameVariable,
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
	var err error
	for suffix, target := range map[string]**goacodegen.NameDeclaration{
		"spec":            &names.spec,
		"decode":          &names.decode,
		"decode-chunk":    &names.decodeChunk,
		"complete":        &names.complete,
		"stream-complete": &names.streamComplete,
	} {
		kind := goacodegen.NameFunction
		prefix := ""
		nameSuffix := ""
		switch suffix {
		case "spec":
			kind = goacodegen.NameVariable
			prefix = "Spec"
		case "decode":
			prefix = "Decode"
		case "decode-chunk":
			prefix = "Decode"
			nameSuffix = "Chunk"
		case "complete":
			prefix = "Complete"
		case "stream-complete":
			prefix = "StreamComplete"
		}
		*target, err = p.public.DeclareDependentName(
			kind,
			constant,
			prefix,
			nameSuffix,
			specNameOrder{packagePath: p.public.ImportPath(), key: "completion:" + name + ":" + suffix},
		)
		if err != nil {
			return err
		}
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
	owners := toolsetOwners(data)
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
				if planned.specs == nil {
					owner := owners[planned.definition]
					if owner == nil {
						return fmt.Errorf("toolset %q has no data for its selected owner", planned.definition.Name)
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
					if err := planned.linkToolTransforms(data.Genpkg, owner); err != nil {
						return err
					}
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

// toolsetOwners returns the toolset used to build each shared package,
// following the service or agent selected in the saved design.
func toolsetOwners(data *GeneratorData) map[*ir.Toolset]*ToolsetData {
	owners := make(map[*ir.Toolset]*ToolsetData)
	for _, service := range data.Services {
		for _, agent := range service.Agents {
			for _, toolset := range agent.AllToolsets {
				definition := toolset.definition
				if toolset.Name != definition.Name || !toolsetMatchesOwner(toolset, definition.Owner) {
					continue
				}
				if current := owners[definition]; current == nil || compareToolsetOwner(toolset, current) < 0 {
					owners[definition] = toolset
				}
			}
		}
	}
	return owners
}

// toolsetMatchesOwner reports whether toolset is the reference named by owner.
func toolsetMatchesOwner(toolset *ToolsetData, owner ir.Owner) bool {
	switch owner.Kind {
	case ir.OwnerKindAgentExport:
		return toolset.Kind == ToolsetKindExported &&
			toolset.Agent.Service.Name == owner.ServiceName &&
			toolset.Agent.Name == owner.AgentName
	case ir.OwnerKindService:
		return toolset.Kind == ToolsetKindUsed &&
			(toolset.ServiceName == owner.ServiceName || toolset.SourceServiceName == owner.ServiceName)
	default:
		panic(fmt.Sprintf("unknown toolset owner kind %q", owner.Kind))
	}
}

// compareToolsetOwner puts matching references in a stable text order.
func compareToolsetOwner(a, b *ToolsetData) int {
	if compared := strings.Compare(a.ServiceName, b.ServiceName); compared != 0 {
		return compared
	}
	if compared := strings.Compare(a.Agent.ID, b.Agent.ID); compared != 0 {
		return compared
	}
	return strings.Compare(a.QualifiedName, b.QualifiedName)
}

// declareToolTypes records the input, result, and server-data types generated
// for tool.
func (p *toolSpecsPackagePlan) declareToolTypes(toolset string, tool *agent.ToolExpr) error {
	owner := &contractTypeOwner{
		Kind:                     contractTypeOwnerTool,
		Name:                     tool.Name,
		QualifiedName:            toolset + "." + tool.Name,
		ScopeName:                toolset,
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
	for _, serverData := range tool.ServerData {
		if serverData == nil || serverData.Schema == nil {
			continue
		}
		if err := p.declareType(owner, serverData.Schema, usageServerData, serverData.Kind); err != nil {
			return err
		}
		names.serverDataTypes[serverData.Kind] = p.types[stableTypeKey(owner, usageServerData, serverData.Kind)]
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
			planned, err := p.declareTransform(qualified+":method-payload", payload.public, tool.Method.Payload, "", adapterPayloadTransformLayout)
			if err != nil {
				return err
			}
			names.methodPayloadTransformPlan = planned
		}
	}
	result := p.types[stableTypeKey(owner, usageResult, "")]
	if result != nil && tool.Method.Result != nil && tool.Method.Result.Type != goaexpr.Empty {
		if err := goacodegen.IsCompatible(tool.Method.Result.Type, result.public.Type, "in", "out"); err == nil {
			planned, err := p.declareTransform(qualified+":tool-result", tool.Method.Result, result.public, "", adapterResultTransformLayout)
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
		planned, err := p.declareTransform(qualified+":server-data:"+serverData.Kind, source, target.public, "", adapterResultTransformLayout)
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
	key := stableTypeKey(owner, usage, qualifier)
	if p.types[key] != nil {
		return nil
	}
	preferred := goacodegen.Goify(owner.Name, true)
	switch usage {
	case usagePayload:
		preferred += "Payload"
	case usageResult:
		preferred += "Result"
	case usageServerData:
		preferred += goacodegen.Goify(qualifier, true) + "ServerData"
	}
	// A generated tool type uses the fields from the source type. Save those
	// same fields for its definition and conversions.
	shape := attribute
	if userType, ok := attribute.Type.(goaexpr.UserType); ok && userType != goaexpr.Empty {
		shape = userType.Attribute()
	}
	public, publicTypes := localizeNestedTypes(shape, false)
	if err := p.declareLocalTypes(p.public, p.publicTypes, p.publicTypeUses, publicTypes, key+":public"); err != nil {
		return err
	}
	publicType := &goaexpr.UserTypeExpr{
		AttributeExpr: stripStructPkgMeta(public),
		TypeName:      preferred,
	}
	publicAttribute := &goaexpr.AttributeExpr{Type: publicType}
	publicDeclaration := goacodegen.NewPreferredName(
		goacodegen.NameType,
		preferred,
		goacodegen.ExportedName,
		specNameOrder{packagePath: p.public.ImportPath(), key: key + ":public"},
	)
	if err := p.public.DeclareName(publicDeclaration, publicType); err != nil {
		return err
	}
	p.publicTypeUses[publicType] = publicDeclaration
	if err := declareAttributeUnions(p.public, p.publicFixed, p.publicUnionErrors, public); err != nil {
		return err
	}

	transportSource := public
	if usage == usagePayload && len(owner.ModelHiddenPayloadFields) > 0 {
		transportSource = modelTransportShape(public, owner.ModelHiddenPayloadFields)
	}
	transport := cloneWithModelJSONTags(transportSource)
	transport, transportTypes := localizeNestedTypes(transport, true)
	if err := p.declareLocalTypes(p.transport, p.transportTypes, p.transportTypeUses, transportTypes, key+":transport"); err != nil {
		return err
	}
	transportType := &goaexpr.UserTypeExpr{
		AttributeExpr: transport,
		TypeName:      preferred + "Transport",
	}
	transportAttribute := &goaexpr.AttributeExpr{Type: transportType}
	transportDeclaration := goacodegen.NewPreferredName(
		goacodegen.NameType,
		transportType.TypeName,
		goacodegen.ExportedName,
		specNameOrder{packagePath: p.transport.ImportPath(), key: key + ":transport"},
	)
	if err := p.transport.DeclareName(transportDeclaration, transportType); err != nil {
		return err
	}
	p.transportTypeUses[transportType] = transportDeclaration
	if err := declareAttributeUnions(p.transport, p.transportFixed, p.transportUnionErrors, transport); err != nil {
		return err
	}

	decode, err := p.declareTransform(key+":decode", transportAttribute, publicAttribute, "decode", codecDecodeTransformLayout)
	if err != nil {
		return err
	}
	encode, err := p.declareTransform(key+":encode", publicAttribute, transportAttribute, "encode", codecEncodeTransformLayout)
	if err != nil {
		return err
	}
	names, err := p.declareTypeNames(key, preferred, publicDeclaration, transportDeclaration)
	if err != nil {
		return err
	}
	p.types[key] = &plannedSpecType{
		publicDeclaration:    publicDeclaration,
		transportDeclaration: transportDeclaration,
		publicShape:          public,
		transportShape:       transport,
		publicTypes:          publicTypes,
		transportTypes:       transportTypes,
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
		allowedObjectKeys:    names.allowedObjectKeys,
		enrichValidation:     names.enrichValidation,
		invalidFieldType:     names.invalidFieldType,
	}
	return nil
}

// declareTypeNames records the variables and functions written with one type.
func (p *toolSpecsPackagePlan) declareTypeNames(key, preferred string, public, transport *goacodegen.NameDeclaration) (*plannedSpecType, error) {
	names := &plannedSpecType{}
	declarePublic := func(kind goacodegen.PackageNameKind, prefix, suffix, role string) (*goacodegen.NameDeclaration, error) {
		return p.public.DeclareDependentName(
			kind,
			public,
			prefix,
			suffix,
			specNameOrder{packagePath: p.public.ImportPath(), key: key + ":" + role},
		)
	}
	var err error
	names.exportedCodec, err = declarePublic(goacodegen.NameVariable, "", "Codec", "codec")
	if err != nil {
		return nil, err
	}
	names.genericCodec = goacodegen.NewPreferredName(
		goacodegen.NameVariable,
		lowerCamel(preferred)+"Codec",
		goacodegen.UnexportedName,
		specNameOrder{packagePath: p.public.ImportPath(), key: key + ":generic-codec"},
	)
	if err := p.public.DeclareName(names.genericCodec); err != nil {
		return nil, err
	}
	names.marshal, err = declarePublic(goacodegen.NameFunction, "Marshal", "", "marshal")
	if err != nil {
		return nil, err
	}
	names.unmarshal, err = declarePublic(goacodegen.NameFunction, "Unmarshal", "", "unmarshal")
	if err != nil {
		return nil, err
	}
	names.fieldDescriptions, err = declarePublic(goacodegen.NameVariable, "", "FieldDescs", "field-descriptions")
	if err != nil {
		return nil, err
	}
	names.fieldJSONTypes, err = declarePublic(goacodegen.NameVariable, "", "FieldJSONTypes", "field-json-types")
	if err != nil {
		return nil, err
	}
	names.allowedObjectKeys, err = declarePublic(goacodegen.NameVariable, "", "FieldAllowedObjectKeys", "allowed-object-keys")
	if err != nil {
		return nil, err
	}
	names.enrichValidation, err = declarePublic(goacodegen.NameFunction, "enrich", "ValidationError", "enrich-validation")
	if err != nil {
		return nil, err
	}
	names.invalidFieldType, err = declarePublic(goacodegen.NameFunction, "invalid", "FieldTypeError", "invalid-field-type")
	if err != nil {
		return nil, err
	}
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
	return names, nil
}

// declareLocalTypes records the nested types that one output package writes.
func (p *toolSpecsPackagePlan) declareLocalTypes(pkg *goacodegen.GeneratedPackage, declared map[string]*goacodegen.NameDeclaration, uses map[goaexpr.UserType]*goacodegen.NameDeclaration, types []*goaexpr.UserTypeExpr, key string) error {
	for _, userType := range types {
		hash := userType.Hash()
		if declaration := declared[hash]; declaration != nil {
			uses[userType] = declaration
			continue
		}
		declaration := goacodegen.NewPreferredName(
			goacodegen.NameType,
			userType.TypeName,
			goacodegen.ExportedName,
			specNameOrder{packagePath: pkg.ImportPath(), key: key + ":type:" + hash},
		)
		if err := pkg.DeclareName(declaration, userType); err != nil {
			return err
		}
		declared[hash] = declaration
		uses[userType] = declaration
		if pkg == p.transport {
			validator, err := pkg.DeclareDependentName(
				goacodegen.NameFunction,
				declaration,
				"Validate",
				"",
				specNameOrder{packagePath: pkg.ImportPath(), key: key + ":validator:" + hash},
			)
			if err != nil {
				return err
			}
			p.transportValidators[hash] = validator
		}
	}
	return nil
}

// declareTransform records the helper functions needed to copy one generated
// type into another and saves each chosen name with its function.
func (p *toolSpecsPackagePlan) declareTransform(key string, source, target *goaexpr.AttributeExpr, prefix string, layout plannedTransformLayout) (*goacodegen.TransformPlan, error) {
	planned, err := goacodegen.NewTransformPlan(source, target, prefix, nil)
	if err != nil {
		return nil, err
	}
	for _, helper := range planned.Helpers() {
		identity, err := p.transformHelperIdentity(helper, layout)
		if err != nil {
			return nil, err
		}
		if existing := p.findTransformHelper(identity, planned); existing != nil {
			existing.plans[planned] = struct{}{}
			if err := planned.BindHelperDeclaration(helper.ID, existing.declaration); err != nil {
				return nil, err
			}
			continue
		}
		namePrefix := prefix
		if namePrefix == "" {
			namePrefix = "transform"
		}
		preferred := lowerCamel(namePrefix + goacodegen.Goify(helper.Source.Type.Name(), true) + "To" + goacodegen.Goify(helper.Target.Type.Name(), true))
		declaration := goacodegen.NewPreferredName(
			goacodegen.NameFunction,
			preferred,
			goacodegen.UnexportedName,
			specNameOrder{
				packagePath: p.public.ImportPath(),
				key:         fmt.Sprintf("%s:%d", key, helper.Occurrence),
			},
		)
		if err := p.public.DeclareName(declaration); err != nil {
			return nil, err
		}
		p.transformHelpers = append(p.transformHelpers, &plannedTransformHelper{
			identity:    identity,
			declaration: declaration,
			plans:       map[*goacodegen.TransformPlan]struct{}{planned: {}},
		})
		if err := planned.BindHelperDeclaration(helper.ID, declaration); err != nil {
			return nil, err
		}
	}
	return planned, nil
}

// declareAttributeUnions records every union reachable from attribute once.
func declareAttributeUnions(pkg *goacodegen.GeneratedPackage, fixed map[string]*goacodegen.NameDeclaration, helpers map[goacodegen.UnionTypeID]*goacodegen.NameDeclaration, attribute *goaexpr.AttributeExpr) error {
	seenTypes := make(map[string]struct{})
	seenUnions := make(map[goacodegen.UnionTypeID]struct{})
	var visit func(*goaexpr.AttributeExpr) error
	visit = func(current *goaexpr.AttributeExpr) error {
		if current == nil || current.Type == nil || current.Type == goaexpr.Empty {
			return nil
		}
		switch actual := current.Type.(type) {
		case goaexpr.UserType:
			if _, ok := seenTypes[actual.ID()]; ok {
				return nil
			}
			seenTypes[actual.ID()] = struct{}{}
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
			identity := goacodegen.NewUnionTypeID(actual)
			if _, ok := seenUnions[identity]; ok {
				return nil
			}
			seenUnions[identity] = struct{}{}
			declaration, err := pkg.DeclareUnion(actual)
			if err != nil {
				return err
			}
			if helpers[identity] == nil {
				helper, err := pkg.DeclareDependentName(
					goacodegen.NameFunction,
					declaration.Declaration(),
					"new",
					"DiscriminatorError",
					specNameOrder{packagePath: pkg.ImportPath(), key: "union-error:" + identity.Hash()},
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
