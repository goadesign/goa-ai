// Package codegen plans a reduced JSON input for each consuming agent.
// The generated decoder returns the provider's existing Go payload; the provider
// keeps its complete schema, codec, and independently generated package.
package codegen

import (
	"fmt"
	"path"
	"slices"

	"goa.design/goa-ai/codegen/ir"
	agentexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// consumerInputPlan records the codecs owned by one consuming reference.
	consumerInputPlan struct {
		packages              *toolSpecsPackagePlan
		provider              *toolSpecsPackagePlan
		reference             *ir.ToolsetRef
		dir                   string
		tools                 []*consumerInputTool
		fieldBindings         []*consumerToolsetFieldBinding
		unknownLookupKeyError *codegen.NameDeclaration
		specs                 *toolSpecsData
		validators            map[consumerValidatorKey]*consumerValidator
		validatorOrder        []*consumerValidator
	}

	// consumerToolsetFieldBinding records a toolset-wide BindField declaration and
	// the tools whose inputs include that field.
	consumerToolsetFieldBinding struct {
		field         string
		goName        string
		hasLookup     bool
		tools         []*consumerInputTool
		local         *codegen.NameDeclaration
		published     *codegen.NameDeclaration
		lookupLocal   *codegen.NameDeclaration
		lookupPublish *codegen.NameDeclaration
	}

	// consumerInputTool retains the provider payload and fields supplied by the
	// application until generated names and field layouts can be linked.
	consumerInputTool struct {
		expression                            *agentexpr.ToolExpr
		owner                                 *contractTypeOwner
		fields                                []string
		model, textOnly                       *typeData
		contextOwner                          *contractTypeOwner
		contextArgs                           *goaexpr.AttributeExpr
		context                               *typeData
		local, published, prepare, modelCodec *codegen.NameDeclaration
		fieldLayouts                          []*codegen.GoTypePlan
		fieldPointers                         []bool
		fieldValidators                       []*consumerValidator
		lookupField                           string
		lookupAttr                            *goaexpr.AttributeExpr
		lookupOwner                           *contractTypeOwner
		lookupModel, lookupTextOnly           *typeData
		lookupModelCodec                      *codegen.NameDeclaration
		lookupLocal, lookupPublished          *codegen.NameDeclaration
		lookupPrepare                         *codegen.NameDeclaration
	}
)

// planConsumerInputs resolves every binding against its compiled provider before
// reserving a consumer package. Invalid declarations cannot reach execution.
func (p *toolSpecsPlan) planConsumerInputs(generation *codegen.Generation, design *ir.Design) error {
	for _, consumer := range design.Agents {
		for _, reference := range consumer.UsedToolsets {
			if len(reference.Expr.InputBindings) == 0 && len(reference.Expr.FieldBindings) == 0 {
				continue
			}
			provider := p.byDir[reference.SpecsDir]
			if provider == nil {
				return fmt.Errorf("consumer %q cannot bind unknown provider %q", consumer.ID, reference.Name)
			}
			expressions, err := toolExpressionsForReference(p.mcp, reference)
			if err != nil {
				return err
			}
			binding := &consumerInputPlan{provider: provider, reference: reference, dir: path.Join(reference.Dir, "inputbinding")}
			for _, declaration := range reference.Expr.InputBindings {
				tool, err := resolveConsumerInput(expressions, declaration)
				if err != nil {
					return fmt.Errorf("consumer %q toolset %q: %w", consumer.ID, reference.Name, err)
				}
				binding.tools = append(binding.tools, tool)
			}
			if err := resolveConsumerFieldBindings(binding, expressions, reference.Expr.FieldBindings); err != nil {
				return fmt.Errorf("consumer %q toolset %q: %w", consumer.ID, reference.Name, err)
			}
			public, err := generation.ClaimPackage(path.Join(reference.PackageImportPath, "inputbinding"))
			if err != nil {
				return err
			}
			transport, err := generation.ClaimPackage(path.Join(public.ImportPath(), "http"))
			if err != nil {
				return err
			}
			binding.packages = newToolSpecsPackagePlan(generation, p.genpkg, public, transport)
			if err := binding.plan(); err != nil {
				return fmt.Errorf("consumer %q input codecs: %w", consumer.ID, err)
			}
			p.consumerInputs = append(p.consumerInputs, binding)
		}
	}
	return nil
}

// resolveConsumerFieldBindings expands toolset-level field bindings across every
// public tool in the toolset whose input object declares the named field.
func resolveConsumerFieldBindings(plan *consumerInputPlan, tools []*agentexpr.ToolExpr, bindings []*agentexpr.FieldBindingExpr) error {
	seenFields := make(map[string]struct{}, len(bindings))
	byTool := make(map[string]*consumerInputTool, len(plan.tools))
	for _, existing := range plan.tools {
		byTool[existing.expression.Name] = existing
	}
	for _, declaration := range bindings {
		if _, ok := seenFields[declaration.Field]; ok {
			return fmt.Errorf("BindField field %q is repeated", declaration.Field)
		}
		seenFields[declaration.Field] = struct{}{}
		group := &consumerToolsetFieldBinding{
			field:     declaration.Field,
			goName:    codegen.Goify(declaration.Field, true),
			hasLookup: declaration.Lookup != nil,
		}
		for _, selected := range tools {
			if isDedicatedContinuation(selected) {
				continue
			}
			object := effectiveObject(selected.Args)
			if object == nil || object.Attribute(declaration.Field) == nil {
				continue
			}
			hidden := modelHiddenPayloadFields(selected)
			if slices.Contains(hidden, declaration.Field) {
				return fmt.Errorf("BindField tool %q field %q is already supplied by execution", selected.Name, declaration.Field)
			}
			if slices.Contains(selected.UIOnlyFields, declaration.Field) {
				return fmt.Errorf("BindField tool %q field %q is owned by the text-only policy", selected.Name, declaration.Field)
			}
			if existing := byTool[selected.Name]; existing != nil {
				return fmt.Errorf("BindField %q conflicts with existing binding on tool %q", declaration.Field, selected.Name)
			}
			tool := &consumerInputTool{
				expression: selected,
				fields:     []string{declaration.Field},
			}
			if declaration.Lookup != nil {
				tool.lookupField = declaration.Field
				tool.lookupAttr = declaration.Lookup
			}
			plan.tools = append(plan.tools, tool)
			byTool[selected.Name] = tool
			group.tools = append(group.tools, tool)
		}
		if len(group.tools) == 0 {
			return fmt.Errorf("BindField %q matches no tools in toolset", declaration.Field)
		}
		plan.fieldBindings = append(plan.fieldBindings, group)
	}
	return nil
}

// resolveConsumerInput checks the declared operation and required top-level fields.
// The provider's existing hidden inputs cannot acquire a second owner.
func resolveConsumerInput(tools []*agentexpr.ToolExpr, binding *agentexpr.InputBindingExpr) (*consumerInputTool, error) {
	var selected *agentexpr.ToolExpr
	for _, tool := range tools {
		if tool.Name == binding.Tool {
			selected = tool
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("BindInput names unknown tool %q", binding.Tool)
	}
	object := effectiveObject(selected.Args)
	if object == nil {
		return nil, fmt.Errorf("BindInput tool %q requires an object input", binding.Tool)
	}
	hidden := modelHiddenPayloadFields(selected)
	for index, name := range binding.Fields {
		if slices.Contains(binding.Fields[:index], name) {
			return nil, fmt.Errorf("BindInput field %q is repeated", name)
		}
		if object.Attribute(name) == nil {
			return nil, fmt.Errorf("BindInput tool %q has no field %q", binding.Tool, name)
		}
		if !selected.Args.IsRequired(name) {
			return nil, fmt.Errorf("BindInput tool %q field %q must be required", binding.Tool, name)
		}
		if slices.Contains(hidden, name) {
			return nil, fmt.Errorf("BindInput tool %q field %q is already supplied by execution", binding.Tool, name)
		}
		if slices.Contains(selected.UIOnlyFields, name) {
			return nil, fmt.Errorf("BindInput tool %q field %q is owned by the text-only policy", binding.Tool, name)
		}
	}
	return &consumerInputTool{expression: selected, fields: slices.Clone(binding.Fields)}, nil
}

// plan imports the provider's retained Go payload layout and generates only the
// consumer's JSON transport, validation, and conversion functions.
func (p *consumerInputPlan) plan() error {
	pkg := p.packages
	p.validators = make(map[consumerValidatorKey]*consumerValidator)
	if err := pkg.declareCompletionPackageNames(); err != nil {
		return err
	}
	hasLookup := false
	for _, tool := range p.tools {
		expression := tool.expression
		tool.owner = &contractTypeOwner{
			Kind:                     contractTypeOwnerConsumerInput,
			Name:                     expression.Name,
			QualifiedName:            p.reference.Name + "." + expression.Name,
			ScopeName:                p.reference.Name,
			ModelHiddenPayloadFields: append(modelHiddenPayloadFields(expression), tool.fields...),
			UIOnlyFields:             slices.Clone(expression.UIOnlyFields),
		}
		original := p.provider.tools[expression.Name].payloadType
		pkg.types[stableTypeKey(tool.owner, usagePayload, "")] = original
		for _, entry := range []struct {
			prefix, suffix string
			target         **codegen.NameDeclaration
		}{
			{"", "ModelInputCodec", &tool.modelCodec}, {"Bind", "Input", &tool.local}, {"Bind", "SourceInput", &tool.published}, {"prepare", "Input", &tool.prepare},
		} {
			visibility := codegen.ExportedName
			if entry.target == &tool.prepare {
				visibility = codegen.UnexportedName
			}
			*entry.target = codegen.NewPreferredName(codegen.NameFunction, entry.prefix+codegen.Goify(expression.Name, true)+entry.suffix, visibility, specNameOrder{packagePath: pkg.public.ImportPath(), key: expression.Name + entry.suffix + entry.prefix})
			if err := pkg.public.DeclareName(*entry.target); err != nil {
				return err
			}
		}
		for _, name := range tool.fields {
			field := effectiveObject(original.publicShape).Attribute(name)
			layouts := original.publicLayout.PlansForOccurrence(field)
			if len(layouts) != 1 {
				return fmt.Errorf("bound field %q has %d Go layouts", name, len(layouts))
			}
			fieldLayout := layouts[0]
			fieldPointer := false
			if fieldLayout.IsPointer() && !goaexpr.IsObject(field.Type) {
				savedValidation := original.publicShape.Validation
				var savedRequired []string
				if savedValidation != nil {
					savedRequired = slices.Clone(savedValidation.Required)
				} else {
					original.publicShape.Validation = &goaexpr.ValidationExpr{}
				}
				if !original.publicShape.IsRequired(name) {
					original.publicShape.Validation.Required = append(original.publicShape.Validation.Required, name)
				}
				requiredPlan, err := p.provider.planDeclaredTypeLayout(original.public, p.provider.public, codegen.GoLayoutPolicy{UseDefault: true, SumType: true})
				original.publicShape.Validation = savedValidation
				if savedValidation != nil {
					savedValidation.Required = savedRequired
				}
				if err != nil {
					return err
				}
				requiredLayouts := requiredPlan.PlansForOccurrence(field)
				if len(requiredLayouts) != 1 {
					return fmt.Errorf("required bound field %q has %d Go layouts", name, len(requiredLayouts))
				}
				fieldLayout = requiredLayouts[0]
				fieldPointer = true
			}
			tool.fieldLayouts = append(tool.fieldLayouts, fieldLayout)
			tool.fieldPointers = append(tool.fieldPointers, fieldPointer)
			validation, err := p.validator(field, fieldLayout)
			if err != nil {
				return err
			}
			tool.fieldValidators = append(tool.fieldValidators, validation)
		}
		for _, dependency := range original.publicLayout.CompleteImportPreferences() {
			if err := pkg.fileImports.publicCodecs.Require(codegen.NewImport(dependency.Name, dependency.Path)); err != nil {
				return err
			}
		}
		for _, usage := range []typeUsage{usageModelPayload, usageTextOnlyPayload} {
			if usage == usageTextOnlyPayload && (expression.RequiresUI || expression.Confirmation != nil || len(expression.UIOnlyFields) == 0) {
				continue
			}
			owner := *tool.owner
			if usage == usageTextOnlyPayload {
				owner.ModelHiddenPayloadFields = append(slices.Clone(owner.ModelHiddenPayloadFields), owner.UIOnlyFields...)
			}
			if err := pkg.declareTypeImports(&owner, expression.Args, usage, specJSONModel); err != nil {
				return err
			}
			if err := pkg.declareType(&owner, expression.Args, usage, "", specJSONModel); err != nil {
				return err
			}
			typ := pkg.types[stableTypeKey(&owner, usage, "")]
			typ.jsonValidator.render = true
		}
		if tool.lookupAttr != nil {
			hasLookup = true
			for _, entry := range []struct {
				prefix, suffix string
				target         **codegen.NameDeclaration
			}{
				{"", "LookupModelInputCodec", &tool.lookupModelCodec},
				{"Bind", "Lookup", &tool.lookupLocal},
				{"Bind", "SourceLookup", &tool.lookupPublished},
				{"prepare", "Lookup", &tool.lookupPrepare},
			} {
				visibility := codegen.ExportedName
				if entry.target == &tool.lookupPrepare {
					visibility = codegen.UnexportedName
				}
				*entry.target = codegen.NewPreferredName(
					codegen.NameFunction,
					entry.prefix+codegen.Goify(expression.Name, true)+entry.suffix,
					visibility,
					specNameOrder{packagePath: pkg.public.ImportPath(), key: expression.Name + entry.suffix + entry.prefix},
				)
				if err := pkg.public.DeclareName(*entry.target); err != nil {
					return err
				}
			}
			lookupOwner := &contractTypeOwner{
				Kind:                     contractTypeOwnerConsumerInput,
				Name:                     expression.Name + "_lookup",
				QualifiedName:            p.reference.Name + "." + expression.Name + ".lookup",
				ScopeName:                p.reference.Name,
				ModelHiddenPayloadFields: modelHiddenPayloadFields(expression),
				LookupPayloadFields: map[string]*goaexpr.AttributeExpr{
					tool.lookupField: tool.lookupAttr,
				},
				UIOnlyFields: slices.Clone(expression.UIOnlyFields),
			}
			tool.lookupOwner = lookupOwner
			pkg.types[stableTypeKey(lookupOwner, usagePayload, "")] = original
			for _, usage := range []typeUsage{usageModelPayload, usageTextOnlyPayload} {
				if usage == usageTextOnlyPayload && (expression.RequiresUI || expression.Confirmation != nil || len(expression.UIOnlyFields) == 0) {
					continue
				}
				owner := *lookupOwner
				if usage == usageTextOnlyPayload {
					owner.ModelHiddenPayloadFields = append(slices.Clone(owner.ModelHiddenPayloadFields), owner.UIOnlyFields...)
				}
				if err := pkg.declareTypeImports(&owner, expression.Args, usage, specJSONModel); err != nil {
					return err
				}
				if err := pkg.declareType(&owner, expression.Args, usage, "", specJSONModel); err != nil {
					return err
				}
				typ := pkg.types[stableTypeKey(&owner, usage, "")]
				typ.jsonValidator.render = true
			}
		}
		contextOwner := *tool.owner
		contextOwner.Name += "_bound_values"
		contextOwner.QualifiedName += ".bound_values"
		contextOwner.ModelHiddenPayloadFields = nil
		for _, field := range *effectiveObject(expression.Args) {
			if !slices.Contains(tool.fields, field.Name) {
				contextOwner.ModelHiddenPayloadFields = append(contextOwner.ModelHiddenPayloadFields, field.Name)
			}
		}
		contextArgs := requiredFieldsShape(expression.Args, tool.fields)
		tool.contextOwner = &contextOwner
		tool.contextArgs = contextArgs
		pkg.types[stableTypeKey(&contextOwner, usagePayload, "")] = original
		if err := pkg.declareTypeImports(&contextOwner, contextArgs, usageModelPayload, specJSONModel); err != nil {
			return err
		}
		if err := pkg.declareType(&contextOwner, contextArgs, usageModelPayload, "", specJSONModel); err != nil {
			return err
		}
		contextType := pkg.types[stableTypeKey(&contextOwner, usageModelPayload, "")]
		contextType.jsonValidator.render = true
	}
	for _, group := range p.fieldBindings {
		if !group.hasUniformFieldType() {
			continue
		}
		for _, entry := range []struct {
			suffix string
			target **codegen.NameDeclaration
		}{
			{"Input", &group.local},
			{"SourceInput", &group.published},
		} {
			*entry.target = codegen.NewPreferredName(
				codegen.NameFunction,
				"Bind"+group.goName+entry.suffix,
				codegen.ExportedName,
				specNameOrder{packagePath: pkg.public.ImportPath(), key: "field:" + group.field + ":" + entry.suffix},
			)
			if err := pkg.public.DeclareName(*entry.target); err != nil {
				return err
			}
		}
		if group.hasLookup {
			for _, entry := range []struct {
				suffix string
				target **codegen.NameDeclaration
			}{
				{"Lookup", &group.lookupLocal},
				{"SourceLookup", &group.lookupPublish},
			} {
				*entry.target = codegen.NewPreferredName(
					codegen.NameFunction,
					"Bind"+group.goName+entry.suffix,
					codegen.ExportedName,
					specNameOrder{packagePath: pkg.public.ImportPath(), key: "field:" + group.field + ":" + entry.suffix},
				)
				if err := pkg.public.DeclareName(*entry.target); err != nil {
					return err
				}
			}
		}
	}
	if hasLookup {
		p.unknownLookupKeyError = codegen.NewPreferredName(
			codegen.NameFunction,
			"unknownLookupKeyError",
			codegen.UnexportedName,
			specNameOrder{packagePath: pkg.public.ImportPath(), key: "helper:unknown-lookup-key-error"},
		)
		if err := pkg.public.DeclareName(p.unknownLookupKeyError); err != nil {
			return err
		}
		for _, imp := range []*codegen.ImportSpec{
			codegen.SimpleImport("encoding/json"),
			codegen.SimpleImport("slices"),
			codegen.SimpleImport("strings"),
		} {
			if err := pkg.fileImports.publicSpecs.Require(imp); err != nil {
				return err
			}
		}
	}
	for _, imp := range []*codegen.ImportSpec{
		codegen.SimpleImport("fmt"), codegen.NewImport("genregistry", "goa.design/goa-ai/registry/gen/registry"),
		codegen.SimpleImport("goa.design/goa-ai/runtime/agent/runtime"), codegen.SimpleImport("goa.design/goa-ai/runtime/agent/tools"),
		codegen.SimpleImport("goa.design/goa-ai/runtime/agent/rawjson"),
	} {
		if err := pkg.fileImports.publicSpecs.Require(imp); err != nil {
			return err
		}
	}
	for _, tool := range p.tools {
		for _, imp := range p.provider.tools[tool.expression.Name].payloadType.publicLayout.CompleteImportPreferences() {
			if err := pkg.fileImports.publicSpecs.Require(codegen.NewImport(imp.Name, imp.Path)); err != nil {
				return err
			}
		}
	}
	if err := pkg.finalizeJSONValidators(); err != nil {
		return err
	}
	if err := pkg.planSharedFileImports(); err != nil {
		return err
	}
	return pkg.finalizeTransformHelpers()
}

// requiredFieldsShape returns a copy of att where all named fields are marked
// required when any of them was optional on att.
func requiredFieldsShape(att *goaexpr.AttributeExpr, fields []string) *goaexpr.AttributeExpr {
	allRequired := true
	for _, name := range fields {
		if !att.IsRequired(name) {
			allRequired = false
			break
		}
	}
	if allRequired {
		return att
	}
	dup := goaexpr.DupAtt(att)
	if ut, ok := dup.Type.(goaexpr.UserType); ok {
		inner := goaexpr.DupAtt(ut.Attribute())
		if len(dup.UserExamples) == 0 {
			dup.UserExamples = inner.ExtractUserExamples()
		}
		dup.Type = inner.Type
		if dup.Validation == nil {
			dup.Validation = inner.Validation
		}
	}
	if dup.Validation == nil {
		dup.Validation = &goaexpr.ValidationExpr{}
	}
	for _, name := range fields {
		if !dup.IsRequired(name) {
			dup.Validation.Required = append(dup.Validation.Required, name)
		}
	}
	return dup
}

// link builds reduced schema and codec data after Goa has chosen all package
// names. It never adds the provider payload to the consumer's emitted types.
func (p *consumerInputPlan) link(api *goaexpr.APIExpr) error {
	if err := p.packages.fileImports.link(); err != nil {
		return err
	}
	builder := newToolSpecBuilder(nil, p.packages, api)
	p.specs = newToolSpecsData(p.packages.genpkg, nil)
	for _, tool := range p.tools {
		var err error
		tool.model, err = builder.typeFor(tool.owner, tool.expression.Args, usageModelPayload)
		if err != nil {
			return err
		}
		p.specs.addType(tool.model)
		tool.context, err = builder.typeFor(tool.contextOwner, tool.contextArgs, usageModelPayload)
		if err != nil {
			return err
		}
		p.specs.addType(tool.context)
		tool.textOnly = tool.model
		if p.packages.types[stableTypeKey(tool.owner, usageTextOnlyPayload, "")] != nil {
			owner := *tool.owner
			owner.ModelHiddenPayloadFields = append(slices.Clone(owner.ModelHiddenPayloadFields), owner.UIOnlyFields...)
			tool.textOnly, err = builder.typeFor(&owner, tool.expression.Args, usageTextOnlyPayload)
			if err != nil {
				return err
			}
			p.specs.addType(tool.textOnly)
		}
		if tool.lookupOwner != nil {
			tool.lookupModel, err = builder.typeFor(tool.lookupOwner, tool.expression.Args, usageModelPayload)
			if err != nil {
				return err
			}
			p.specs.addType(tool.lookupModel)
			tool.lookupTextOnly = tool.lookupModel
			if p.packages.types[stableTypeKey(tool.lookupOwner, usageTextOnlyPayload, "")] != nil {
				owner := *tool.lookupOwner
				owner.ModelHiddenPayloadFields = append(slices.Clone(owner.ModelHiddenPayloadFields), owner.UIOnlyFields...)
				tool.lookupTextOnly, err = builder.typeFor(&owner, tool.expression.Args, usageTextOnlyPayload)
				if err != nil {
					return err
				}
				p.specs.addType(tool.lookupTextOnly)
			}
		}
	}
	keys := make([]string, 0, len(builder.types))
	for key := range builder.types {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		p.specs.addType(builder.types[key])
	}
	p.specs.TransportUnions = builder.transportUnionTypes()
	p.specs.CodecTransformHelpers = builder.codecTransformHelpers
	p.specs.JSONValidators = materializeJSONValidators(p.packages.jsonValidators)
	p.specs.JSONDocumentValidators = materializeJSONDocumentValidators(p.packages.jsonDocumentValidators)
	return nil
}

// hasUniformFieldType reports whether every tool matched by this toolset-level
// field binding shares an identical Go parameter type for the bound field. When
// tools declare distinct Go types for the same field name, toolset-wide convenience
// wrappers cannot compile and only per-tool binding functions are emitted.
func (b *consumerToolsetFieldBinding) hasUniformFieldType() bool {
	if len(b.tools) <= 1 {
		return true
	}
	first := b.tools[0].fieldLayouts[0]
	for _, tool := range b.tools[1:] {
		if !sameParameterType(first, tool.fieldLayouts[0]) {
			return false
		}
	}
	return true
}

// sameParameterType reports whether two field layout plans produce the same Go
// parameter type reference when linked in a consumer input binding function,
// ignoring enclosing-struct field comments, descriptions, and JSON tags.
func sameParameterType(a, b *codegen.GoTypePlan) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind() != b.Kind() || a.ReferenceIsPointer() != b.ReferenceIsPointer() || a.Owner() != b.Owner() {
		return false
	}
	switch a.Kind() {
	case codegen.GoNamed:
		if a.TypeDeclaration() != nil || b.TypeDeclaration() != nil {
			return a.TypeDeclaration() == b.TypeDeclaration()
		}
		return a.Link("", identityQualifier).Name() == b.Link("", identityQualifier).Name()
	case codegen.GoUnion:
		if a.UnionDeclaration() != nil || b.UnionDeclaration() != nil {
			return a.UnionDeclaration() == b.UnionDeclaration()
		}
		return a.Link("", identityQualifier).Name() == b.Link("", identityQualifier).Name()
	case codegen.GoPrimitive, codegen.GoEmpty, codegen.GoServiceError:
		return a.Link("", identityQualifier).Name() == b.Link("", identityQualifier).Name()
	case codegen.GoArray:
		return a.Elem().IsPointer() == b.Elem().IsPointer() && sameParameterType(a.Elem(), b.Elem())
	case codegen.GoMap:
		return a.Key().IsPointer() == b.Key().IsPointer() &&
			a.Elem().IsPointer() == b.Elem().IsPointer() &&
			sameParameterType(a.Key(), b.Key()) &&
			sameParameterType(a.Elem(), b.Elem())
	case codegen.GoStruct:
		aFields, bFields := a.Fields(), b.Fields()
		if len(aFields) != len(bFields) {
			return false
		}
		for i := range aFields {
			af, bf := aFields[i], bFields[i]
			if af.FieldName(true) != bf.FieldName(true) ||
				af.IsPointer() != bf.IsPointer() ||
				af.Tag() != bf.Tag() ||
				af.Description() != bf.Description() ||
				!sameParameterType(af, bf) {
				return false
			}
		}
		return true
	default:
		return a.Equivalent(b)
	}
}

// identityQualifier returns importPath unchanged so LinkedGoType comparisons can
// compare canonical package-qualified type names before file import aliases are linked.
func identityQualifier(importPath string) string {
	if importPath == "" {
		return "_"
	}
	return importPath
}
