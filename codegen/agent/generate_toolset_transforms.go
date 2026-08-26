// Package codegen builds the functions that copy values between a tool package and
// its Goa service. The names and conversion steps are saved before files are
// written, so every file uses the same final names.
package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
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
func (p *toolSpecsPackagePlan) linkToolTransforms(ts *ToolsetData, services *service.ServicesData) error {
	specs := p.specs
	scope := specs.Scope
	svc := ts.SourceService
	serviceAttributor := services.ServiceAttributor(svc.Name, p.public.ImportPath())
	for _, tool := range ts.Tools {
		if !tool.IsMethodBacked {
			continue
		}
		names := p.tools[tool.Name]
		if names.methodPayloadTransformPlan != nil {
			planned := p.transformFor(names.methodPayloadTransformPlan)
			payload := names.payloadType.public
			source := codegen.NewAttributeContext(false, false, true, "", scope)
			source, err := source.WithGoTypeLayout(planned.sourceLayout.Link(p.public.ImportPath(), p.public.ImportName))
			if err != nil {
				return fmt.Errorf("link method payload conversion for tool %q: %w", tool.QualifiedName, err)
			}
			target := serviceAttributeContext(serviceAttributor)
			body, helpers, err := renderToolTransform(planned.plan, source, target)
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
			planned := p.transformFor(names.toolResultTransformPlan)
			result := names.resultType.public
			source := serviceAttributeContext(serviceAttributor)
			target := codegen.NewAttributeContext(false, false, true, "", scope)
			target, err := target.WithGoTypeLayout(planned.targetLayout.Link(p.public.ImportPath(), p.public.ImportName))
			if err != nil {
				return fmt.Errorf("link tool result conversion for tool %q: %w", tool.QualifiedName, err)
			}
			body, helpers, err := renderToolTransform(planned.plan, source, target)
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
			planned := p.transformFor(plan)
			sourceAttribute := tool.MethodResultAttr.Find(serverData.MethodResultField)
			targetAttribute := names.serverDataTypes[serverData.Kind].public
			source := serviceAttributeContext(serviceAttributor)
			target := codegen.NewAttributeContext(false, false, true, "", scope)
			target, err := target.WithGoTypeLayout(planned.targetLayout.Link(p.public.ImportPath(), p.public.ImportName))
			if err != nil {
				return fmt.Errorf("link server data conversion for tool %q kind %q: %w", tool.QualifiedName, serverData.Kind, err)
			}
			body, helpers, err := renderToolTransform(planned.plan, source, target)
			if err != nil {
				return fmt.Errorf("build server data conversion for tool %q kind %q: %w", tool.QualifiedName, serverData.Kind, err)
			}
			specs.adapterHelpers = codegen.AppendHelpers(specs.adapterHelpers, helpers)
			specs.adapterTransforms = append(specs.adapterTransforms, transformFuncData{
				Name:               names.serverDataTransforms[serverData.Kind].Name(),
				ParamTypeRef:       serviceAttributor.Ref(sourceAttribute, ""),
				ResultTypeRef:      scope.GoFullTypeRef(targetAttribute, ""),
				NilInputReturnsNil: serverDataSourceMayBeNil(tool.MethodResultAttr, serverData.MethodResultField, sourceAttribute),
				Body:               body,
			})
		}
	}

	if len(specs.adapterTransforms) > 0 {
		specs.adapterImports = importsForPaths(p.public, p.transformImportPaths)
	}
	return nil
}

// serviceAttributeContext uses Goa's final type and field names for service
// values written in the tool package.
func serviceAttributeContext(attributor codegen.Attributor) *codegen.AttributeContext {
	context := codegen.NewAttributeContext(false, false, true, "", attributor.Scope())
	context.Scope = attributor
	return context
}

// renderToolTransform uses the type names assigned to one saved conversion.
func renderToolTransform(plan *codegen.TransformPlan, source, target *codegen.AttributeContext) (string, []*codegen.TransformFunctionData, error) {
	if err := plan.BindContexts(source, target); err != nil {
		return "", nil, err
	}
	return plan.Render("in", "out", false)
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
