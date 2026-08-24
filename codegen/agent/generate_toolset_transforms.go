// This file builds the functions that copy values between a tool package and
// its Goa service. The names and conversion steps are saved before files are
// written, so every file uses the same final names.
package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"

	"goa.design/goa-ai/codegen/naming"
	"goa.design/goa-ai/codegen/shared"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// toolsetAdapterTransformsFile writes functions that copy values between tool
// types and service types.
func toolsetAdapterTransformsFile(ts *ToolsetData) *codegen.File {
	if len(ts.specs.adapterTransforms) == 0 {
		return nil
	}
	return &codegen.File{
		Path: filepath.Join(ts.SpecsDir, "transforms.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" adapter transforms", ts.SpecsPackageName, ts.specs.adapterImports),
			{
				Name:   "tool-transforms",
				Source: agentsTemplates.Read(toolTransformsFileT),
				Data: transformsFileData{
					Functions: ts.specs.adapterTransforms,
					Helpers:   ts.specs.adapterHelpers,
				},
			},
		},
	}
}

// linkToolTransforms writes each saved conversion with the final type and
// function names.
func (p *toolSpecsPackagePlan) linkToolTransforms(genpkg string, ts *ToolsetData) error {
	specs := p.specs
	scope := specs.Scope
	svc := ts.SourceService
	svcAlias := servicePkgAlias(svc)
	svcImport := shared.JoinImportPath(genpkg, svc.PathName)
	extraImports := make(map[string]*codegen.ImportSpec)
	for _, tool := range ts.Tools {
		if !tool.IsMethodBacked {
			continue
		}
		names := p.tools[tool.Name]
		if names.methodPayloadTransformPlan != nil {
			payload := names.payloadType.public
			addTransformImports(extraImports, genpkg, tool.MethodPayloadAttr, payload)
			source := codegen.NewAttributeContext(false, false, true, "", scope)
			target := codegen.NewAttributeContext(false, false, true, svcAlias, scope)
			body, helpers, err := renderToolTransform(names.methodPayloadTransformPlan, source, target)
			if err != nil {
				return fmt.Errorf("build method payload conversion for tool %q: %w", tool.QualifiedName, err)
			}
			specs.adapterHelpers = codegen.AppendHelpers(specs.adapterHelpers, helpers)
			specs.adapterTransforms = append(specs.adapterTransforms, transformFuncData{
				Name:          names.methodPayloadTransform.Name(),
				ParamTypeRef:  scope.GoFullTypeRef(payload, ""),
				ResultTypeRef: tool.MethodPayloadTypeRef,
				Body:          body,
			})
		}

		if names.toolResultTransformPlan != nil {
			result := names.resultType.public
			addTransformImports(extraImports, genpkg, tool.MethodResultAttr, result)
			source := codegen.NewAttributeContext(false, false, true, svcAlias, scope)
			target := codegen.NewAttributeContext(false, false, true, "", scope)
			body, helpers, err := renderToolTransform(names.toolResultTransformPlan, source, target)
			if err != nil {
				return fmt.Errorf("build tool result conversion for tool %q: %w", tool.QualifiedName, err)
			}
			specs.adapterHelpers = codegen.AppendHelpers(specs.adapterHelpers, helpers)
			specs.adapterTransforms = append(specs.adapterTransforms, transformFuncData{
				Name:          names.toolResultTransform.Name(),
				ParamTypeRef:  tool.MethodResultTypeRef,
				ResultTypeRef: scope.GoFullTypeRef(result, ""),
				Body:          body,
			})
		}

		for _, serverData := range tool.ServerData {
			plan := names.serverDataTransformPlans[serverData.Kind]
			if plan == nil {
				continue
			}
			sourceAttribute := tool.MethodResultAttr.Find(serverData.MethodResultField)
			targetAttribute := names.serverDataTypes[serverData.Kind].public
			addTransformImports(extraImports, genpkg, sourceAttribute, targetAttribute)
			sourcePackage := typeRefDefaultPackage(svcAlias, sourceAttribute)
			source := codegen.NewAttributeContext(false, false, true, sourcePackage, scope)
			target := codegen.NewAttributeContext(false, false, true, "", scope)
			body, helpers, err := renderToolTransform(plan, source, target)
			if err != nil {
				return fmt.Errorf("build server data conversion for tool %q kind %q: %w", tool.QualifiedName, serverData.Kind, err)
			}
			specs.adapterHelpers = codegen.AppendHelpers(specs.adapterHelpers, helpers)
			specs.adapterTransforms = append(specs.adapterTransforms, transformFuncData{
				Name:               names.serverDataTransforms[serverData.Kind].Name(),
				ParamTypeRef:       scope.GoFullTypeRef(sourceAttribute, sourcePackage),
				ResultTypeRef:      scope.GoFullTypeRef(targetAttribute, ""),
				NilInputReturnsNil: serverDataSourceMayBeNil(tool.MethodResultAttr, serverData.MethodResultField, sourceAttribute),
				Body:               body,
			})
		}
	}

	if len(specs.adapterTransforms) > 0 {
		specs.adapterImports = adapterTransformImports(svcAlias, svcImport, extraImports)
	}
	return nil
}

// addTransformImports records packages used by the supplied types.
func addTransformImports(imports map[string]*codegen.ImportSpec, genpkg string, attributes ...*expr.AttributeExpr) {
	for _, attribute := range attributes {
		for _, item := range shared.GatherAttributeImports(genpkg, attribute) {
			if item != nil && item.Path != "" {
				imports[item.Path] = item
			}
		}
	}
}

// renderToolTransform uses the type names assigned to one saved conversion.
func renderToolTransform(plan *codegen.TransformPlan, source, target *codegen.AttributeContext) (string, []*codegen.TransformFunctionData, error) {
	if err := plan.BindContexts(source, target); err != nil {
		return "", nil, err
	}
	return plan.Render("in", "out", false)
}

// adapterTransformImports returns imports in path order and gives each package
// a different local name.
func adapterTransformImports(serviceAlias, serviceImport string, extra map[string]*codegen.ImportSpec) []*codegen.ImportSpec {
	imports := []*codegen.ImportSpec{{Name: serviceAlias, Path: serviceImport}}
	used := map[string]struct{}{serviceAlias: {}}
	paths := make([]string, 0, len(extra))
	for importPath := range extra {
		if importPath != "" && importPath != serviceImport {
			paths = append(paths, importPath)
		}
	}
	slices.Sort(paths)
	for _, importPath := range paths {
		item := *extra[importPath]
		if item.Name != "" {
			if _, ok := used[item.Name]; ok {
				panic(fmt.Sprintf("agent codegen: import name %q is used by more than one package", item.Name))
			}
			used[item.Name] = struct{}{}
		} else {
			item.Name = uniqueImportAlias(used, naming.SanitizeToken(path.Base(item.Path), "pkg"))
		}
		imports = append(imports, &item)
	}
	return imports
}

// uniqueImportAlias returns a package name that is not already used.
func uniqueImportAlias(used map[string]struct{}, base string) string {
	if base == "" {
		base = "pkg"
	}
	alias := base
	for suffix := 2; ; suffix++ {
		if _, ok := used[alias]; !ok {
			used[alias] = struct{}{}
			return alias
		}
		alias = fmt.Sprintf("%s%d", base, suffix)
	}
}

// typeRefDefaultPackage returns the package recorded on att when it has one.
func typeRefDefaultPackage(defaultPackage string, att *expr.AttributeExpr) string {
	if location := codegen.UserTypeLocation(att.Type); location != nil && location.PackageName() != "" {
		return location.PackageName()
	}
	return defaultPackage
}

// serverDataSourceMayBeNil reports whether an optional method result field can
// contain nil.
func serverDataSourceMayBeNil(result *expr.AttributeExpr, field string, source *expr.AttributeExpr) bool {
	if result.IsRequired(field) {
		return false
	}
	if !expr.IsPrimitive(source.Type) {
		return true
	}
	return result.IsPrimitivePointer(field, false)
}

