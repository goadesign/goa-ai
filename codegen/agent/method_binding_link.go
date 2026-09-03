// Package codegen links every tool to its final generated names, then links
// method-backed tools to the Goa service fields used by each output package.
package codegen

import (
	"fmt"
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// linkToolsetNames copies the names chosen for one reusable specs package into
// every service or agent reference to that toolset.
func linkToolsetNames(planned *toolSpecsPackagePlan, toolset *ToolsetData) error {
	for _, tool := range toolset.Tools {
		names := planned.tools[tool.Name]
		if names == nil {
			return fmt.Errorf("link tool %q: planned names are missing", tool.QualifiedName)
		}
		tool.ConstName = names.constant.Name()
		tool.SpecVar = names.spec.Name()
		if names.payloadType != nil {
			tool.PayloadTypeName = names.payloadType.publicDeclaration.Name()
			tool.PayloadPointer = names.payloadType.publicLayout.ReferenceIsPointer()
			tool.PayloadCodecName = names.payloadType.exportedCodec.Name()
		}
		if names.resultType != nil {
			tool.ResultTypeName = names.resultType.publicDeclaration.Name()
			tool.ResultCodecName = names.resultType.exportedCodec.Name()
		}
		if names.inject != nil {
			tool.InjectFunc = names.inject.Name()
			tool.DecodeFunc = names.decode.Name()
		}
		if names.methodPayloadTransform != nil {
			tool.MethodPayloadTransform = names.methodPayloadTransform.Name()
		}
		if names.toolResultTransform != nil {
			tool.ToolResultTransform = names.toolResultTransform.Name()
		}
		if names.bounds != nil {
			tool.BoundsFunc = names.bounds.Name()
		}
		for _, serverData := range tool.ServerData {
			if declaration := names.serverDataTransforms[serverData.Kind]; declaration != nil {
				serverData.Transform = declaration.Name()
			}
			if plannedType := names.serverDataTypes[serverData.Kind]; plannedType != nil {
				serverData.CodecName = plannedType.exportedCodec.Name()
			}
		}
		for _, injected := range tool.Injected {
			layout := names.injectedFieldLayouts[injected.Name]
			if layout == nil {
				return fmt.Errorf("link tool %q injected field %q: planned field is missing", tool.QualifiedName, injected.Name)
			}
			linked := layout.Link(planned.public.ImportPath(), planned.public.ImportName)
			injected.GoFieldName = linked.Field(true)
			targetType := linked.Name()
			if targetType != goacodegen.GoNativeTypeName(goaexpr.String) {
				injected.TargetType = targetType
			}
			validation := names.injectedFieldValidations[injected.Name]
			if validation == nil {
				return fmt.Errorf("link tool %q injected field %q: planned validation is missing", tool.QualifiedName, injected.Name)
			}
			linkedValidation, err := validation.Link(linked)
			if err != nil {
				return fmt.Errorf("link tool %q injected field %q validation: %w", tool.QualifiedName, injected.Name, err)
			}
			injected.ValidationCode = strings.TrimSpace(linkedValidation.Render("v", injected.Name))
		}
	}
	return nil
}

// linkMethodToolset fills one shared tool package with final Goa service names
// and the public payload field names chosen by the saved tool package plan.
func (p *toolSpecsPlan) linkMethodToolset(planned *toolSpecsPackagePlan, toolset *ToolsetData, services *service.ServicesData) error {
	if !toolsetHasMethodTools(toolset) {
		return nil
	}
	if toolset.SourceService == nil {
		return fmt.Errorf("link method toolset %q: source service is missing", toolset.QualifiedName)
	}
	attributor := services.ServiceAttributor(toolset.SourceService.Name, planned.public.ImportPath())
	for _, tool := range toolset.Tools {
		if !tool.IsMethodBacked {
			continue
		}
		bindMethodTypeRefs(tool, attributor)
		if err := p.linkMethodResultFields(tool); err != nil {
			return err
		}
	}
	return nil
}

// bindMethodTypeRefs asks Goa to write payload and result references for the
// package that will contain the generated call.
func bindMethodTypeRefs(tool *ToolData, attributor goacodegen.Attributor) {
	if tool.MethodPayloadAttr != nil {
		tool.MethodPayloadTypeRef = attributor.Ref(tool.MethodPayloadAttr, "")
	}
	if tool.MethodResultAttr != nil {
		tool.MethodResultTypeRef = attributor.Ref(tool.MethodResultAttr, "")
	}
}

// linkMethodResultFields replaces each DSL field name with the selector from
// Goa's retained result layout.
func (p *toolSpecsPlan) linkMethodResultFields(tool *ToolData) error {
	if tool.method == nil || tool.MethodResultAttr == nil {
		return nil
	}
	layout, err := p.service.MethodResultLayout(tool.method)
	if err != nil {
		return fmt.Errorf("link method tool %q result layout: %w", tool.QualifiedName, err)
	}
	for _, field := range boundsProjectionFields(tool.Bounds) {
		if field == nil {
			continue
		}
		selector, err := resultFieldSelector(layout, tool.MethodResultAttr, field.Name)
		if err != nil {
			return fmt.Errorf("link method tool %q bounds field %q: %w", tool.QualifiedName, field.Name, err)
		}
		field.Name = selector
	}
	for _, serverData := range tool.ServerData {
		if serverData.MethodResultField == "" {
			continue
		}
		selector, err := resultFieldSelector(layout, tool.MethodResultAttr, serverData.MethodResultField)
		if err != nil {
			return fmt.Errorf("link method tool %q server data field %q: %w", tool.QualifiedName, serverData.MethodResultField, err)
		}
		serverData.MethodResultFieldName = selector
	}
	return nil
}

// resultFieldSelector returns the final Go selector for one result field.
func resultFieldSelector(layout *goacodegen.GoTypePlan, result *goaexpr.AttributeExpr, name string) (string, error) {
	attribute := result.Find(name)
	if attribute == nil {
		return "", fmt.Errorf("field is not present in the method result")
	}
	for _, field := range layout.Fields() {
		if field.MatchesOccurrence(attribute) {
			return field.FieldName(true), nil
		}
	}
	return "", fmt.Errorf("field has no finalized Goa layout")
}

// boundsProjectionFields lists the result fields read by one bounds helper.
func boundsProjectionFields(bounds *ToolBoundsData) []*ToolBoundsFieldData {
	if bounds == nil || bounds.Projection == nil {
		return nil
	}
	projection := bounds.Projection
	return []*ToolBoundsFieldData{
		projection.Returned,
		projection.Total,
		projection.Truncated,
		projection.NextCursor,
		projection.RefinementHint,
	}
}

// importsForPaths returns each final import once in its planned order.
func importsForPaths(pkg *goacodegen.GeneratedPackage, paths []string) []*goacodegen.ImportSpec {
	seen := make(map[string]struct{}, len(paths))
	imports := make([]*goacodegen.ImportSpec, 0, len(paths))
	for _, importPath := range paths {
		if importPath == "" {
			continue
		}
		if _, ok := seen[importPath]; ok {
			continue
		}
		seen[importPath] = struct{}{}
		imports = append(imports, pkg.Import(importPath))
	}
	return imports
}

// toolsetHasMethodTools reports whether a toolset needs Goa service bindings.
func toolsetHasMethodTools(toolset *ToolsetData) bool {
	for _, tool := range toolset.Tools {
		if tool.IsMethodBacked {
			return true
		}
	}
	return false
}
