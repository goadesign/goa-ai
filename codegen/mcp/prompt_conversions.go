// Package codegen plans and writes content conversions with Goa's saved service
// declarations. Runtime code selects only the authored union branch; field
// names, type references, binary encoding, and nested conversions are generated.
package codegen

import (
	"fmt"

	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

// planContentConversions reserves functions and complete result imports before
// Goa freezes names. The result schema has already passed the authoring checks.
func planContentConversions(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	if len(data.MethodPrompts) == 0 {
		return nil
	}
	pkg := generation.Package(data.mcpImportPath)
	var content *expr.AttributeExpr
	for _, method := range prepared.mcpService.Methods {
		if method.Name != "prompts/get" {
			continue
		}
		messages := expr.AsArray(expr.AsObject(method.Result.Type).Attribute("messages").Type)
		content = expr.AsObject(messages.ElemType.Type).Attribute("content")
	}
	if content == nil {
		return fmt.Errorf("method-backed prompts require the generated prompts/get method")
	}
	imports := codegen.NewGeneratedImportPlan(pkg)
	conversions := make(map[*expr.AttributeExpr]*contentConversion)
	for index, prompt := range data.MethodPrompts {
		result, layout, err := planMCPResult(services, prompt.prompt.Method)
		if err != nil {
			return err
		}
		method := *prompt.prompt.Method
		method.Result = result
		selected := *prompt.prompt
		selected.Method = &method
		if err := selected.Validate(); err != nil {
			return fmt.Errorf("prompt %q selected result view: %w", prompt.Name, err)
		}
		prompt.resultAttribute, prompt.resultLayout = result, layout
		messages := expr.AsObject(result.Type).Attribute("messages")
		if messages == nil {
			return fmt.Errorf("prompt %q selected view omits messages", prompt.Name)
		}
		message := expr.AsObject(expr.AsArray(messages.Type).ElemType.Type)
		prompt.conversion, err = buildContentConversion(message.Attribute("content"), content, true)
		if err != nil {
			return fmt.Errorf("prompt %q selected result content: %w", prompt.Name, err)
		}
		if err := imports.AddCompleteType(layout); err != nil {
			return err
		}
		for _, importPath := range imports.Paths() {
			if importPath != data.mcpImportPath {
				data.serverImportPaths = append(data.serverImportPaths, importPath)
			}
		}
		if existing := conversions[prompt.conversion.attribute]; existing != nil {
			prompt.conversion = existing
			continue
		}
		conversions[prompt.conversion.attribute] = prompt.conversion
		if err := planContentConversion(generation, pkg, prompt.conversion, content, layout, fmt.Sprintf("convertPrompt%dContent", index)); err != nil {
			return err
		}
		contentConversionNeeds(data, prompt.conversion)
	}
	return nil
}

// contentConversionNeeds records the imports and metadata checks used by every
// declared variant, including variants inside an embedded resource.
func contentConversionNeeds(data *AdapterData, conversion *contentConversion) {
	for _, branch := range conversion.branches {
		data.NeedsContentBytes = data.NeedsContentBytes || branch.bytesField != ""
		data.NeedsContentMeta = data.NeedsContentMeta || branch.metaField
		object := expr.AsObject(branch.attribute.Type)
		if branch.name == contentResourceLink && object.Attribute("size") != nil {
			data.NeedsContentNumbers = true
		}
		if annotations := object.Attribute("annotations"); annotations != nil && expr.AsObject(annotations.Type).Attribute("priority") != nil {
			data.NeedsContentNumbers = true
		}
		if branch.nested != nil {
			contentConversionNeeds(data, branch.nested)
		}
	}
}

// planContentConversion records one converter and its field transforms.
// Byte and nested-union fields have distinct protocol representations and are
// emitted separately from ordinary Goa field conversions.
func planContentConversion(generation *codegen.Generation, pkg *codegen.GeneratedPackage, conversion *contentConversion, target *expr.AttributeExpr, resultLayout *codegen.GoTypePlan, name string) error {
	conversion.target = target
	declaration := codegen.NewExactName(codegen.NameFunction, name)
	if err := pkg.DeclareName(declaration); err != nil {
		return err
	}
	conversion.declaration = declaration
	occurrences := resultLayout.PlansForOccurrence(conversion.attribute)
	if len(occurrences) != 1 {
		return fmt.Errorf("content layout has %d occurrences", len(occurrences))
	}
	conversion.sourceLayout = occurrences[0]
	conversion.unionAttribute = conversion.attribute
	for {
		named, ok := conversion.unionAttribute.Type.(expr.UserType)
		if !ok {
			break
		}
		conversion.unionAttribute = named.Attribute()
	}
	unionLayout := conversion.sourceLayout
	if unionLayout.UnionDeclaration() == nil {
		matches := resultLayout.PlansForOccurrence(conversion.unionAttribute)
		if len(matches) != 1 {
			return fmt.Errorf("content must have one planned union definition")
		}
		unionLayout = matches[0]
	}
	conversion.unionLayout = unionLayout
	conversion.sourcePackage = generation.Package(unionLayout.UnionDeclaration().PackagePath())
	for index, branch := range conversion.branches {
		object := expr.AsObject(branch.attribute.Type)
		fields := make(expr.Object, 0, len(*object))
		for _, field := range *object {
			if field.Name == branch.bytesField || branch.nested != nil && field.Name == contentResourceField {
				continue
			}
			fields = append(fields, field)
		}
		projection := &expr.AttributeExpr{Type: &fields, Validation: expr.EffectiveValidation(branch.attribute)}
		transform, err := codegen.NewTransformPlan(projection, conversion.target, name, nil)
		if err != nil {
			return err
		}
		for helperIndex, helper := range transform.Helpers() {
			helperDeclaration := codegen.NewExactName(codegen.NameFunction, fmt.Sprintf("%sBranch%dHelper%d", name, index, helperIndex))
			if err := pkg.DeclareName(helperDeclaration); err != nil {
				return err
			}
			if err := transform.BindHelperDeclaration(helper.ID, helperDeclaration); err != nil {
				return err
			}
		}
		branch.transform = transform
		if branch.nested != nil {
			if err := planContentConversion(generation, pkg, branch.nested, expr.AsObject(target.Type).Attribute(contentResourceField), resultLayout, fmt.Sprintf("%sResource%d", name, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

// bindContentConversions supplies final service names and private codec fields.
// The adapter then fills arguments without serializing and parsing them again.
func bindContentConversions(services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	targetScope := services.ServiceAttributor(planned.prepared.mcpService.Name, data.mcpImportPath)
	for _, prompt := range data.MethodPrompts {
		sourceScope := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
		if prompt.Endpoint.ProjectedResult {
			sourceScope = services.ViewAttributor(planned.prepared.userService.Name, data.mcpImportPath)
		}
		result := expr.AsObject(prompt.resultAttribute.Type)
		messagesAttribute := result.Attribute("messages")
		message := expr.AsObject(expr.AsArray(messagesAttribute.Type).ElemType.Type)
		prompt.MessagesField = sourceScope.Field(messagesAttribute, "messages", true)
		role := message.Attribute("role")
		prompt.RoleField = sourceScope.Field(role, "role", true)
		roleLayouts := prompt.resultLayout.PlansForOccurrence(role)
		if len(roleLayouts) != 1 {
			return fmt.Errorf("prompt role must have one planned field")
		}
		prompt.RolePointer = roleLayouts[0].IsPointer()
		prompt.ContentField = sourceScope.Field(message.Attribute("content"), "content", true)
		if description := result.Attribute("description"); description != nil {
			// Goa's saved layout records whether this selected description
			// has a presence pointer. Keep that exact field representation.
			matches := prompt.resultLayout.PlansForOccurrence(description)
			if len(matches) != 1 {
				return fmt.Errorf("prompt description must have one planned field")
			}
			prompt.DescriptionPointer = matches[0].IsPointer()
			prompt.DescriptionField = sourceScope.Field(description, "description", true)
		}
		if prompt.HasPayload {
			value := planned.methodCodecs[prompt.prompt.Method.Name].payload
			transport, err := value.TransportTypeName(data.mcpImportPath, data.mcpPackage.ImportName)
			if err != nil {
				return err
			}
			prompt.PayloadTransportRef = transport
			prompt.PayloadConstructor = value.TransportConstructorDeclaration().Name()
			object := expr.AsObject(prompt.prompt.Method.Payload.Type)
			for _, argument := range prompt.Arguments {
				field, err := value.TransportField(object.Attribute(argument.Name), argument.Name, data.mcpImportPath, data.mcpPackage.ImportName)
				if err != nil {
					return err
				}
				argument.Selector, argument.TypeRef = field.Selector, field.ValueTypeRef
			}
		}
		if prompt.conversion.data == nil {
			if err := bindContentConversion(data, prompt.conversion, sourceScope, targetScope); err != nil {
				return err
			}
		}
		prompt.ContentConversion = prompt.conversion.declaration.Name()
	}
	return nil
}

// bindContentConversion renders the typed branch copies after all names
// are final. A validated service value cannot select an undeclared branch.
func bindContentConversion(data *AdapterData, conversion *contentConversion, sourceScope, targetScope codegen.Attributor) error {
	source := &codegen.AttributeContext{Scope: sourceScope, UseDefault: true, Pointer: conversion.sourceLayout.Policy().Pointer}
	target := &codegen.AttributeContext{Scope: targetScope, UseDefault: true}
	linked := conversion.sourceLayout.Link(data.mcpImportPath, data.mcpPackage.ImportName)
	conversion.data = &contentConversionData{
		Name: conversion.declaration.Name(), SourceRef: linked.RefWithPointer(conversion.sourceLayout.IsPointer()), TargetRef: targetScope.Ref(conversion.target, ""),
		HasType: expr.AsObject(conversion.target.Type).Attribute("type") != nil,
	}
	conversion.data.Value = "value"
	unionRef := conversion.unionLayout.Link(data.mcpImportPath, data.mcpPackage.ImportName).RefWithPointer(conversion.sourceLayout.IsPointer())
	if unionRef != conversion.data.SourceRef {
		conversion.data.UnionRef = unionRef
		conversion.data.Value = "selectedContent"
	}
	for _, branch := range conversion.branches {
		sourceBranch, err := conversion.sourcePackage.UnionBranch(conversion.unionAttribute, branch.name)
		if err != nil {
			return err
		}
		if err := branch.transform.BindContexts(source, target); err != nil {
			return err
		}
		code, helpers, err := branch.transform.Render("selected", "out", true)
		if err != nil {
			return err
		}
		conversion.data.Helpers = codegen.AppendHelpers(conversion.data.Helpers, helpers)
		rendered := &contentBranchData{
			Name:   branch.name,
			Kind:   data.mcpPackage.ImportName(conversion.sourcePackage.ImportPath()) + "." + sourceBranch.KindConst(),
			Getter: "As" + codegen.Goify(branch.name, true), Transform: code,
		}
		object := expr.AsObject(branch.attribute.Type)
		rendered.CheckSize = branch.name == contentResourceLink && object.Attribute("size") != nil
		if annotations := object.Attribute("annotations"); annotations != nil {
			rendered.CheckPriority = expr.AsObject(annotations.Type).Attribute("priority") != nil
		}
		if branch.bytesField != "" {
			bytesAttribute := object.Attribute(branch.bytesField)
			rendered.BytesField = sourceScope.Field(bytesAttribute, branch.bytesField, true)
			matches := conversion.sourceLayout.PlansForOccurrence(bytesAttribute)
			if len(matches) != 1 {
				return fmt.Errorf("content bytes must have one planned field")
			}
			rendered.BytesPointer = matches[0].IsPointer()
			rendered.TargetBytesField = targetScope.Field(expr.AsObject(conversion.target.Type).Attribute(branch.bytesField), branch.bytesField, true)
		}
		if branch.metaField {
			rendered.MetaField = sourceScope.Field(object.Attribute("_meta"), "_meta", true)
		}
		if branch.nested != nil {
			rendered.ResourceField = sourceScope.Field(object.Attribute(contentResourceField), contentResourceField, true)
			if err := bindContentConversion(data, branch.nested, sourceScope, targetScope); err != nil {
				return err
			}
			rendered.NestedConversion = branch.nested.declaration.Name()
		}
		conversion.data.Branches = append(conversion.data.Branches, rendered)
	}
	data.ContentConversions = append(data.ContentConversions, conversion.data)
	return nil
}
