// Package codegen plans every import and helper name used when generated agent
// code calls a Goa service method. Linking later reads only Goa's retained type
// layouts and declarations.
package codegen

import (
	"fmt"

	"goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// planMethodBindings reserves imports and helper names before Goa makes every
// package name final.
func (p *toolSpecsPlan) planMethodBindings(design *ir.Design, servicePlan *service.Plan) error {
	p.service = servicePlan
	for _, definition := range design.Toolsets {
		reference := definition.Owner.Ref
		tools, err := toolExpressionsForDefinition(p.mcp, definition)
		if err != nil {
			return err
		}
		if !hasMethodTool(tools) {
			continue
		}
		serviceImport, err := methodToolsetServiceImport(servicePlan, reference)
		if err != nil {
			return err
		}
		specs, err := p.packageFor(reference.SpecsDir)
		if err != nil {
			return err
		}
		if err := planProviderImports(specs, servicePlan, serviceImport, tools); err != nil {
			return fmt.Errorf("plan method toolset %q provider imports: %w", reference.QualifiedName, err)
		}
	}
	return nil
}

// methodToolsetServiceImport returns the Goa service package used by a
// method-backed toolset.
func methodToolsetServiceImport(servicePlan *service.Plan, reference *ir.ToolsetRef) (*goacodegen.ImportSpec, error) {
	if reference.SourceService == nil || reference.SourceService.Expr == nil {
		return nil, fmt.Errorf("plan method toolset %q: source service is missing", reference.QualifiedName)
	}
	serviceImport, _, err := servicePlan.ServicePackageImports(reference.SourceService.Expr)
	if err != nil {
		return nil, fmt.Errorf("plan method toolset %q service import: %w", reference.QualifiedName, err)
	}
	return serviceImport, nil
}

// planProviderImports reserves imports shared by the service provider and its
// generated service conversion functions.
func planProviderImports(
	planned *toolSpecsPackagePlan,
	servicePlan *service.Plan,
	serviceImport *goacodegen.ImportSpec,
	tools []*agent.ToolExpr,
) error {
	pkg := planned.public
	fixed := []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"),
		goacodegen.SimpleImport("errors"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/toolregistry"),
		goacodegen.NewImport("goa", "goa.design/goa/v3/pkg"),
	}
	if hasBoundsTool(tools) {
		fixed = append(fixed, goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent"))
	}
	if hasInjectedMethodTool(tools) {
		fixed = append(fixed, goacodegen.NewImport("runtime", "goa.design/goa-ai/runtime/agent/runtime"))
	}
	if err := requirePackageImports(pkg, fixed); err != nil {
		return err
	}
	if err := pkg.ReserveGeneratedImport(serviceImport); err != nil {
		return err
	}
	_, err := reserveMethodLayoutImports(pkg, servicePlan, serviceImport.Path, tools)
	if err != nil {
		return err
	}
	planned.serviceImportPath = serviceImport.Path
	planned.providerImportPaths = append(importSpecPaths(fixed), serviceImport.Path)
	planned.transformImportPaths, err = methodTransformImportPaths(planned, servicePlan, tools)
	if err != nil {
		return err
	}
	return nil
}

// methodTransformImportPaths returns only packages referenced by conversions
// that will be written to transforms.go.
func methodTransformImportPaths(planned *toolSpecsPackagePlan, servicePlan *service.Plan, tools []*agent.ToolExpr) ([]string, error) {
	seen := make(map[string]struct{})
	var paths []string
	addLayout := func(layout *goacodegen.GoTypePlan) {
		if layoutUsesOwner(layout) {
			appendImportPath(&paths, seen, layout.Owner())
		}
		for _, preference := range layout.ImportPreferences() {
			appendImportPath(&paths, seen, preference.Path)
		}
	}
	for _, tool := range tools {
		if tool.Method == nil {
			continue
		}
		names := planned.tools[tool.Name]
		if names.methodPayloadTransformPlan != nil {
			layout, err := servicePlan.MethodPayloadLayout(tool.Method)
			if err != nil {
				return nil, err
			}
			addLayout(layout)
		}
		if names.toolResultTransformPlan != nil || len(names.serverDataTransformPlans) > 0 {
			layout, err := servicePlan.MethodResultLayout(tool.Method)
			if err != nil {
				return nil, err
			}
			addLayout(layout)
		}
	}
	return paths, nil
}

// appendImportPath appends a non-empty package path once.
func appendImportPath(paths *[]string, seen map[string]struct{}, importPath string) {
	if importPath == "" {
		return
	}
	if _, ok := seen[importPath]; ok {
		return
	}
	seen[importPath] = struct{}{}
	*paths = append(*paths, importPath)
}

// reserveMethodLayoutImports submits the packages retained by Goa's payload
// and result layouts. No attribute tree is walked a second time.
func reserveMethodLayoutImports(
	pkg *goacodegen.GeneratedPackage,
	servicePlan *service.Plan,
	serviceImportPath string,
	tools []*agent.ToolExpr,
) ([]string, error) {
	seen := map[string]struct{}{serviceImportPath: {}}
	var paths []string
	for _, tool := range tools {
		if tool.Method == nil {
			continue
		}
		layouts, err := methodLayouts(servicePlan, tool.Method)
		if err != nil {
			return nil, err
		}
		for _, layout := range layouts {
			if layoutUsesOwner(layout) {
				owner := layout.Owner()
				if _, ok := seen[owner]; !ok {
					seen[owner] = struct{}{}
					if err := pkg.ReserveGeneratedImport(goacodegen.NewImport("", owner)); err != nil {
						return nil, err
					}
					paths = append(paths, owner)
				}
			}
			for _, preference := range layout.ImportPreferences() {
				if _, ok := seen[preference.Path]; ok {
					continue
				}
				seen[preference.Path] = struct{}{}
				if err := pkg.ReserveGeneratedImport(goacodegen.NewImport(preference.Name, preference.Path)); err != nil {
					return nil, err
				}
				paths = append(paths, preference.Path)
			}
		}
	}
	return paths, nil
}

// layoutUsesOwner reports whether the top-level Go type is declared in its
// owning generated package.
func layoutUsesOwner(layout *goacodegen.GoTypePlan) bool {
	switch layout.Kind() {
	case goacodegen.GoStruct, goacodegen.GoNamed, goacodegen.GoUnion:
		return true
	case goacodegen.GoPrimitive, goacodegen.GoArray, goacodegen.GoMap,
		goacodegen.GoEmpty, goacodegen.GoServiceError:
		return false
	}
	panic(fmt.Sprintf("unknown Goa type layout %q", layout.Kind()))
}

// methodLayouts returns the retained field layouts for one service method.
func methodLayouts(servicePlan *service.Plan, method *goaexpr.MethodExpr) ([]*goacodegen.GoTypePlan, error) {
	layouts := make([]*goacodegen.GoTypePlan, 0, 2)
	if method.Payload != nil && method.Payload.Type != goaexpr.Empty {
		layout, err := servicePlan.MethodPayloadLayout(method)
		if err != nil {
			return nil, err
		}
		layouts = append(layouts, layout)
	}
	if method.Result != nil && method.Result.Type != goaexpr.Empty {
		layout, err := servicePlan.MethodResultLayout(method)
		if err != nil {
			return nil, err
		}
		layouts = append(layouts, layout)
	}
	return layouts, nil
}

func requirePackageImports(pkg *goacodegen.GeneratedPackage, specs []*goacodegen.ImportSpec) error {
	for _, spec := range specs {
		if err := pkg.RequireImport(spec); err != nil {
			return err
		}
	}
	return nil
}

func importSpecPaths(specs []*goacodegen.ImportSpec) []string {
	paths := make([]string, 0, len(specs))
	for _, spec := range specs {
		paths = append(paths, spec.Path)
	}
	return paths
}

func hasMethodTool(tools []*agent.ToolExpr) bool {
	for _, tool := range tools {
		if tool.Method != nil {
			return true
		}
	}
	return false
}

func hasBoundsTool(tools []*agent.ToolExpr) bool {
	for _, tool := range tools {
		if tool.Method != nil && tool.Bounds != nil {
			return true
		}
	}
	return false
}

func hasServerDataTool(tools []*agent.ToolExpr) bool {
	for _, tool := range tools {
		if tool.Method == nil {
			continue
		}
		for _, data := range tool.ServerData {
			if data != nil && data.Source != nil && data.Source.MethodResultField != "" {
				return true
			}
		}
	}
	return false
}

func hasInjectedMethodTool(tools []*agent.ToolExpr) bool {
	for _, tool := range tools {
		if tool.Method != nil && len(tool.InjectedFields) > 0 {
			return true
		}
	}
	return false
}
