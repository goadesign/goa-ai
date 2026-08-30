// Package codegen plans every declaration written into an agent-local toolset
// helper package. One plan owns all files in the package so tool names, service
// executor helpers, and MCP executors cannot choose conflicting Go names.
package codegen

import (
	"fmt"
	"sort"
	"strings"

	agentir "goa.design/goa-ai/codegen/ir"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// toolsetHelperPackagesPlan stores one plan for each agent-local helper package.
	toolsetHelperPackagesPlan struct {
		byPath map[string]*toolsetHelperPackagePlan
	}

	// toolsetHelperPackagePlan stores every declaration and import used by the
	// files emitted into one helper package.
	toolsetHelperPackagePlan struct {
		pkg                *goacodegen.GeneratedPackage
		reference          *agentir.ToolsetRef
		toolset            *ToolsetData
		fixed              map[string]*goacodegen.NameDeclaration
		tools              map[string]*plannedHelperToolNames
		serviceConstructor *goacodegen.NameDeclaration
		mcpConstructor     *goacodegen.NameDeclaration
		serviceImportPath  string
		specsImportPath    string
		usedImports        []*goacodegen.ImportSpec
		serviceImports     []*goacodegen.ImportSpec
		serviceTypeImports []string
		mcpImports         []*goacodegen.ImportSpec
		usedTools          *usedToolsetFileData
		serviceExecutor    *serviceExecutorData
		mcpExecutor        *mcpExecutorData
	}

	// plannedHelperToolNames stores the declaration family emitted for one tool.
	plannedHelperToolNames struct {
		constant     *goacodegen.NameDeclaration
		payloadAlias *goacodegen.NameDeclaration
		resultAlias  *goacodegen.NameDeclaration
		call         *goacodegen.NameDeclaration
		callerOption *goacodegen.NameDeclaration
		callerField  string
		bounds       *goacodegen.NameDeclaration
	}

	// toolsetHelperNameOrder keeps collision results stable across design order.
	toolsetHelperNameOrder struct {
		packagePath string
		key         string
	}
)

const (
	helperServiceConfigName           = "seCfg"
	helperExecOptionName              = "ExecOpt"
	helperToolInterceptorName         = "ToolInterceptor"
	helperToolInterceptorFuncName     = "ToolInterceptorFunc"
	helperExecOptionFuncName          = "execOptFunc"
	helperWithPayloadMapperName       = "WithPayloadMapper"
	helperWithResultMapperName        = "WithResultMapper"
	helperWithInterceptorsName        = "WithInterceptors"
	helperWithClientName              = "WithClient"
	helperFailedServiceToolResultName = "failedServiceToolResult"
	helperFailedServiceCallResultName = "failedServiceCallResult"
	helperInvalidServiceToolCallName  = "invalidServiceToolCall"
	helperFailedMCPToolResultName     = "failedMCPToolResult"
)

// planToolsetHelperPackages claims each helper package once and declares every
// name before Goa freezes the generation.
func planToolsetHelperPackages(generation *goacodegen.Generation, design *agentir.Design, specs *toolSpecsPlan, servicePlan *service.Plan) (*toolsetHelperPackagesPlan, error) {
	planned := &toolsetHelperPackagesPlan{byPath: make(map[string]*toolsetHelperPackagePlan)}
	for _, agent := range design.Agents {
		references := append(append([]*agentir.ToolsetRef{}, agent.UsedToolsets...), agent.ExportedToolsets...)
		for _, reference := range references {
			tools, err := toolExpressionsForReference(specs.mcp, reference)
			if err != nil {
				return nil, err
			}
			mcpBacked := reference.Provider != nil && reference.Provider.Kind == agentexpr.ProviderMCP
			methodBacked := !mcpBacked && hasMethodTool(tools)
			if !methodBacked && !mcpBacked {
				continue
			}
			if existing := planned.byPath[reference.PackageImportPath]; existing != nil {
				if existing.reference.Definition != reference.Definition || toolsetProviderKind(existing.reference) != toolsetProviderKind(reference) {
					return nil, fmt.Errorf("helper package %q has incompatible toolset references", reference.PackageImportPath)
				}
				continue
			}
			pkg, err := generation.ClaimPackage(reference.PackageImportPath)
			if err != nil {
				return nil, fmt.Errorf("plan toolset %q helper package: %w", reference.QualifiedName, err)
			}
			packagePlan := &toolsetHelperPackagePlan{
				pkg:       pkg,
				reference: reference,
				fixed:     make(map[string]*goacodegen.NameDeclaration),
				tools:     make(map[string]*plannedHelperToolNames),
			}
			if methodBacked {
				if err := packagePlan.declareToolNames(agent, tools); err != nil {
					return nil, fmt.Errorf("plan toolset %q helper names: %w", reference.QualifiedName, err)
				}
				if err := packagePlan.planMethodImports(servicePlan, tools); err != nil {
					return nil, err
				}
			}
			if mcpBacked {
				if err := packagePlan.declareMCPNames(agent); err != nil {
					return nil, err
				}
				if err := packagePlan.planMCPImports(); err != nil {
					return nil, err
				}
			}
			planned.byPath[reference.PackageImportPath] = packagePlan
		}
	}
	return planned, nil
}

// declareToolNames records the stable service API and the names emitted for
// every tool. Tools bound to Goa methods also receive service caller helpers.
func (p *toolsetHelperPackagePlan) declareToolNames(agent *agentir.Agent, tools []*agentexpr.ToolExpr) error {
	callerFields := goacodegen.NewNameScope()
	callerFields.Unique("mapPayload")
	callerFields.Unique("mapResult")
	callerFields.Unique("injectors")
	methodTools := make([]*agentexpr.ToolExpr, 0, len(tools))
	for _, tool := range tools {
		if tool.Method != nil {
			methodTools = append(methodTools, tool)
		}
	}
	sort.Slice(methodTools, func(i, j int) bool {
		return methodTools[i].Name < methodTools[j].Name
	})
	callerFieldNames := make(map[string]string, len(methodTools))
	for _, tool := range methodTools {
		callerFieldNames[tool.Name] = callerFields.Unique(lowerCamel(tool.Name) + "Caller")
	}
	exact := map[goacodegen.PackageNameKind][]string{
		goacodegen.NameType: {
			helperServiceConfigName,
			helperExecOptionName,
			helperToolInterceptorName,
			helperToolInterceptorFuncName,
			helperExecOptionFuncName,
		},
		goacodegen.NameFunction: {
			helperWithPayloadMapperName,
			helperWithResultMapperName,
			helperWithInterceptorsName,
			helperWithClientName,
			helperFailedServiceToolResultName,
			helperFailedServiceCallResultName,
			helperInvalidServiceToolCallName,
		},
	}
	if err := declareExactNames(p.pkg, p.fixed, exact); err != nil {
		return err
	}
	var err error
	p.serviceConstructor, err = p.declarePreferred(
		goacodegen.NameFunction,
		"New"+goacodegen.Goify(agent.Name, true)+goacodegen.Goify(p.reference.Slug, true)+"Exec",
		goacodegen.ExportedName,
		"service-constructor",
	)
	if err != nil {
		return err
	}
	for _, tool := range tools {
		constant, err := p.declarePreferred(goacodegen.NameConstant, tool.Name, goacodegen.ExportedName, tool.Name+":constant")
		if err != nil {
			return err
		}
		base := goacodegen.Goify(tool.Name, true)
		names := &plannedHelperToolNames{constant: constant}
		names.payloadAlias, err = p.declarePreferred(goacodegen.NameType, base+"Payload", goacodegen.ExportedName, tool.Name+":payload")
		if err != nil {
			return err
		}
		if tool.Return != nil && tool.Return.Type != nil && tool.Return.Type != goaexpr.Empty {
			names.resultAlias, err = p.declarePreferred(goacodegen.NameType, base+"Result", goacodegen.ExportedName, tool.Name+":result")
			if err != nil {
				return err
			}
		}
		names.call, err = p.declarePreferred(goacodegen.NameFunction, "New"+base+"Call", goacodegen.ExportedName, tool.Name+":call")
		if err != nil {
			return err
		}
		if tool.Method != nil {
			names.callerOption, err = p.declarePreferred(goacodegen.NameFunction, "With"+base, goacodegen.ExportedName, tool.Name+":caller")
			if err != nil {
				return err
			}
			names.callerField = callerFieldNames[tool.Name]
		}
		if tool.Method != nil && tool.Bounds != nil {
			names.bounds, err = p.declarePreferred(goacodegen.NameFunction, "init"+base+"Bounds", goacodegen.UnexportedName, tool.Name+":bounds")
			if err != nil {
				return err
			}
		}
		p.tools[tool.Name] = names
	}
	return nil
}

// declareMCPNames records the MCP constructor and its private failure helper.
func (p *toolsetHelperPackagePlan) declareMCPNames(agent *agentir.Agent) error {
	if err := declareExactNames(p.pkg, p.fixed, map[goacodegen.PackageNameKind][]string{
		goacodegen.NameFunction: {helperFailedMCPToolResultName},
	}); err != nil {
		return err
	}
	var err error
	p.mcpConstructor, err = p.declarePreferred(
		goacodegen.NameFunction,
		"New"+goacodegen.Goify(agent.Name, true)+goacodegen.Goify(p.reference.Slug, true)+"MCPExecutor",
		goacodegen.ExportedName,
		"mcp-constructor",
	)
	return err
}

// planMethodImports reserves every package used by the method helper files.
func (p *toolsetHelperPackagePlan) planMethodImports(servicePlan *service.Plan, tools []*agentexpr.ToolExpr) error {
	serviceImport, err := methodToolsetServiceImport(servicePlan, p.reference)
	if err != nil {
		return err
	}
	used := []*goacodegen.ImportSpec{
		goacodegen.NewImport("tools", "goa.design/goa-ai/runtime/agent/tools"),
		goacodegen.NewImport("planner", "goa.design/goa-ai/runtime/agent/planner"),
		goacodegen.NewImport(p.reference.SpecsPackageName+"specs", p.reference.SpecsImportPath),
	}
	serviceImports := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"), goacodegen.SimpleImport("errors"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.NewImport("planner", "goa.design/goa-ai/runtime/agent/planner"),
		goacodegen.NewImport("runtime", "goa.design/goa-ai/runtime/agent/runtime"),
		goacodegen.NewImport("tools", "goa.design/goa-ai/runtime/agent/tools"),
	}
	if hasBoundsTool(tools) {
		serviceImports = append(serviceImports, goacodegen.NewImport("agent", "goa.design/goa-ai/runtime/agent"))
	}
	if hasServerDataTool(tools) {
		serviceImports = append(serviceImports,
			goacodegen.SimpleImport("encoding/json"),
			goacodegen.NewImport("rawjson", "goa.design/goa-ai/runtime/agent/rawjson"),
			goacodegen.SimpleImport("goa.design/goa-ai/runtime/toolregistry"),
		)
	}
	if err := requirePackageImports(p.pkg, append(append([]*goacodegen.ImportSpec{}, used...), serviceImports...)); err != nil {
		return err
	}
	if err := p.pkg.ReserveGeneratedImport(serviceImport); err != nil {
		return err
	}
	if err := p.pkg.ReserveGeneratedImport(goacodegen.NewImport(p.reference.SpecsPackageName+"specs", p.reference.SpecsImportPath)); err != nil {
		return err
	}
	typeImports, err := reserveMethodLayoutImports(p.pkg, servicePlan, serviceImport.Path, tools)
	if err != nil {
		return err
	}
	p.serviceImportPath = serviceImport.Path
	p.specsImportPath = p.reference.SpecsImportPath
	p.usedImports = used
	serviceImports = append(serviceImports, serviceImport, goacodegen.NewImport(p.reference.SpecsPackageName+"specs", p.reference.SpecsImportPath))
	p.serviceImports = serviceImports
	p.serviceTypeImports = typeImports
	return nil
}

// planMCPImports reserves every package used by mcp_executor.go.
func (p *toolsetHelperPackagePlan) planMCPImports() error {
	imports := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"), goacodegen.SimpleImport("encoding/json"), goacodegen.SimpleImport("errors"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/planner"),
		goacodegen.NewImport("runtime", "goa.design/goa-ai/runtime/agent/runtime"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/telemetry"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/tools"),
		goacodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
		goacodegen.NewImport(p.reference.SpecsPackageName, p.reference.SpecsImportPath),
	}
	if err := requirePackageImports(p.pkg, imports); err != nil {
		return err
	}
	p.specsImportPath = p.reference.SpecsImportPath
	p.mcpImports = imports
	return nil
}

// link builds each helper package once and copies only the constructor names
// needed by agent registration into its references.
func (p *toolsetHelperPackagesPlan) link(data *GeneratorData, services *service.ServicesData) error {
	for _, serviceData := range data.Services {
		for _, agent := range serviceData.Agents {
			for _, toolset := range agent.AllToolsets {
				planned := p.byPath[toolset.PackageImportPath]
				if planned == nil {
					continue
				}
				if planned.matches(toolset) && planned.toolset == nil {
					if err := planned.link(toolset, services); err != nil {
						return err
					}
				}
				if planned.mcpConstructor != nil {
					toolset.MCPExecutorConstructor = planned.mcpConstructor.Name()
				}
			}
		}
	}
	for path, planned := range p.byPath {
		if planned.toolset == nil {
			return fmt.Errorf("helper package %q was not linked to its toolset", path)
		}
	}
	return nil
}

// matches reports whether toolset is the exact reference that selected the
// package's names and file contents during planning.
func (p *toolsetHelperPackagePlan) matches(toolset *ToolsetData) bool {
	return toolset.Kind == toolsetKindFromIR(p.reference.Kind) &&
		toolset.QualifiedName == p.reference.QualifiedName
}

// link builds the final records consumed by the three helper file templates.
func (p *toolsetHelperPackagePlan) link(toolset *ToolsetData, services *service.ServicesData) error {
	p.toolset = toolset
	if p.serviceConstructor != nil {
		attributor := services.ServiceAttributor(toolset.SourceService.Name, p.pkg.ImportPath())
		tools := make([]*ToolData, len(toolset.Tools))
		serviceTools := make([]*serviceExecutorToolData, len(toolset.Tools))
		used := make([]*usedToolRenderData, 0, len(toolset.specs.tools))
		entries := make(map[string]*toolEntry, len(toolset.specs.tools))
		for _, entry := range toolset.specs.tools {
			entries[entry.Name] = entry
		}
		for index, original := range toolset.Tools {
			tool := *original
			bindMethodTypeRefs(&tool, attributor)
			names := p.tools[tool.Name]
			if names == nil {
				return fmt.Errorf("toolset %q helper tool %q was not planned", toolset.QualifiedName, tool.Name)
			}
			tool.ConstName = names.constant.Name()
			if names.bounds != nil {
				tool.BoundsFunc = names.bounds.Name()
			}
			entry := entries[tool.QualifiedName]
			if entry == nil {
				return fmt.Errorf("toolset %q helper spec for %q is missing", toolset.QualifiedName, tool.QualifiedName)
			}
			used = append(used, &usedToolRenderData{
				toolEntry:    entry,
				ConstName:    names.constant.Name(),
				PayloadAlias: names.payloadAlias.Name(),
				CallFunc:     names.call.Name(),
			})
			if names.resultAlias != nil {
				used[len(used)-1].ResultAlias = names.resultAlias.Name()
			}
			if names.callerOption != nil {
				tool.HelperCallerOption = names.callerOption.Name()
			}
			tools[index] = &tool
			serviceTools[index] = &serviceExecutorToolData{
				ToolData:    &tool,
				CallerField: names.callerField,
			}
		}
		p.usedTools = &usedToolsetFileData{
			PackageName: toolset.PackageName,
			SpecsAlias:  p.pkg.ImportName(p.specsImportPath),
			Toolset:     toolset,
			Tools:       used,
			Imports:     linkedPlannedImports(p.pkg, p.usedImports),
		}
		serviceImports := linkedPlannedImports(p.pkg, p.serviceImports)
		serviceImports = append(serviceImports, importsForPaths(p.pkg, p.serviceTypeImports)...)
		p.serviceExecutor = &serviceExecutorData{
			Imports:           serviceImports,
			ServiceClientRef:  p.pkg.ImportName(p.serviceImportPath) + "." + toolset.SourceService.ClientDeclaration.Name(),
			SpecsPackageAlias: p.pkg.ImportName(p.specsImportPath),
			Constructor:       p.serviceConstructor.Name(),
			Names:             linkedServiceExecutorNames(p),
			Tools:             serviceTools,
		}
	}
	if p.mcpConstructor != nil {
		p.mcpExecutor = &mcpExecutorData{
			Imports:     linkedPlannedImports(p.pkg, p.mcpImports),
			Constructor: p.mcpConstructor.Name(),
			Failure:     p.fixed[helperFailedMCPToolResultName].Name(),
			SpecsAlias:  p.pkg.ImportName(p.specsImportPath),
		}
	}
	return nil
}

// linkedPlannedImports returns the imports chosen by Goa for one generated file.
func linkedPlannedImports(pkg *goacodegen.GeneratedPackage, imports []*goacodegen.ImportSpec) []*goacodegen.ImportSpec {
	linked := make([]*goacodegen.ImportSpec, 0, len(imports))
	seen := make(map[string]struct{}, len(imports))
	for _, spec := range imports {
		if _, ok := seen[spec.Path]; ok {
			continue
		}
		seen[spec.Path] = struct{}{}
		linked = append(linked, pkg.Import(spec.Path))
	}
	return linked
}

// toolsetProviderKind returns the provider kind used to decide whether two
// references can share one generated helper package.
func toolsetProviderKind(reference *agentir.ToolsetRef) agentexpr.ProviderKind {
	if reference.Provider == nil {
		return agentexpr.ProviderLocal
	}
	return reference.Provider.Kind
}

func linkedServiceExecutorNames(p *toolsetHelperPackagePlan) serviceExecutorNames {
	return serviceExecutorNames{
		ConfigType:          p.fixed[helperServiceConfigName].Name(),
		OptionType:          p.fixed[helperExecOptionName].Name(),
		InterceptorType:     p.fixed[helperToolInterceptorName].Name(),
		InterceptorFuncType: p.fixed[helperToolInterceptorFuncName].Name(),
		OptionFuncType:      p.fixed[helperExecOptionFuncName].Name(),
		WithPayloadMapper:   p.fixed[helperWithPayloadMapperName].Name(),
		WithResultMapper:    p.fixed[helperWithResultMapperName].Name(),
		WithInterceptors:    p.fixed[helperWithInterceptorsName].Name(),
		WithClient:          p.fixed[helperWithClientName].Name(),
		FailedToolResult:    p.fixed[helperFailedServiceToolResultName].Name(),
		FailedCallResult:    p.fixed[helperFailedServiceCallResultName].Name(),
		InvalidToolCall:     p.fixed[helperInvalidServiceToolCallName].Name(),
	}
}

func (p *toolsetHelperPackagePlan) declarePreferred(kind goacodegen.PackageNameKind, preferred string, visibility goacodegen.PackageNameVisibility, key string) (*goacodegen.NameDeclaration, error) {
	declaration := goacodegen.NewPreferredName(kind, preferred, visibility, p.order(key))
	if err := p.pkg.DeclareName(declaration); err != nil {
		return nil, err
	}
	return declaration, nil
}

func (p *toolsetHelperPackagePlan) order(key string) toolsetHelperNameOrder {
	return toolsetHelperNameOrder{packagePath: p.pkg.ImportPath(), key: key}
}

// ComparePackageName orders helper declarations by package and purpose.
func (o toolsetHelperNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(toolsetHelperNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.key, right.key)
}
