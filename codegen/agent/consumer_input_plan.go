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
		packages       *toolSpecsPackagePlan
		provider       *toolSpecsPackagePlan
		reference      *ir.ToolsetRef
		dir            string
		tools          []*consumerInputTool
		specs          *toolSpecsData
		validators     map[consumerValidatorKey]*consumerValidator
		validatorOrder []*consumerValidator
	}

	// consumerInputTool retains the provider payload and fields supplied by the
	// application until generated names and field layouts can be linked.
	consumerInputTool struct {
		expression                            *agentexpr.ToolExpr
		owner                                 *contractTypeOwner
		fields                                []string
		model, textOnly                       *typeData
		contextOwner                          *contractTypeOwner
		context                               *typeData
		local, published, prepare, modelCodec *codegen.NameDeclaration
		fieldLayouts                          []*codegen.GoTypePlan
		fieldValidators                       []*consumerValidator
	}
)

// planConsumerInputs resolves every binding against its compiled provider before
// reserving a consumer package. Invalid declarations cannot reach execution.
func (p *toolSpecsPlan) planConsumerInputs(generation *codegen.Generation, design *ir.Design) error {
	for _, consumer := range design.Agents {
		for _, reference := range consumer.UsedToolsets {
			if len(reference.Expr.InputBindings) == 0 {
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
			tool.fieldLayouts = append(tool.fieldLayouts, layouts[0])
			validation, err := p.validator(field, layouts[0])
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
		contextOwner := *tool.owner
		contextOwner.Name += "_bound_values"
		contextOwner.QualifiedName += ".bound_values"
		contextOwner.ModelHiddenPayloadFields = nil
		for _, field := range *effectiveObject(expression.Args) {
			if !slices.Contains(tool.fields, field.Name) {
				contextOwner.ModelHiddenPayloadFields = append(contextOwner.ModelHiddenPayloadFields, field.Name)
			}
		}
		tool.contextOwner = &contextOwner
		pkg.types[stableTypeKey(&contextOwner, usagePayload, "")] = original
		if err := pkg.declareTypeImports(&contextOwner, expression.Args, usageModelPayload, specJSONModel); err != nil {
			return err
		}
		if err := pkg.declareType(&contextOwner, expression.Args, usageModelPayload, "", specJSONModel); err != nil {
			return err
		}
		contextType := pkg.types[stableTypeKey(&contextOwner, usageModelPayload, "")]
		contextType.jsonValidator.render = true
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
		tool.context, err = builder.typeFor(tool.contextOwner, tool.expression.Args, usageModelPayload)
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
