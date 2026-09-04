// Package codegen turns Goa and Goa-AI designs into generated agent code.
//
// This file records the imports used by each generated tool and completion
// file. Goa chooses the final package names after every file has recorded the
// packages it uses.
package codegen

import (
	"slices"

	"goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

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
		hasInjectValidation := false
		for _, tool := range tools {
			for _, name := range tool.InjectedFields {
				validation := p.tools[tool.Name].injectedFieldValidations[name]
				preferences := validation.ImportPreferences()
				if len(preferences) > 0 {
					hasInjectValidation = true
				}
				for _, preference := range preferences {
					injectImports = append(injectImports, goacodegen.NewImport(preference.Name, preference.Path))
				}
				if _, metaBacked := injectedFieldSource(name); !metaBacked {
					hasLabelInject = true
				}
			}
		}
		if hasLabelInject || hasInjectValidation {
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
		goacodegen.SimpleImport("goa.design/goa-ai/runtime/agent/tools"),
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
	hasFieldMetadata := false
	for key, planned := range p.types {
		if len(buildFieldMetadata(planned.transportShape)) > 0 {
			hasFieldMetadata = true
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
	if hasValidation || hasFieldMetadata {
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
