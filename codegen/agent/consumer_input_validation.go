// Package codegen validates bound fields before typed values enter JSON
// conversion. Goa's retained field layout supplies the rules and nested
// validator calls, so nil required values return errors before conversion.
package codegen

import (
	"fmt"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	consumerValidatorKey struct {
		attribute   *expr.AttributeExpr
		declaration *codegen.TypeDeclaration
	}
	consumerValidator struct {
		name       *codegen.NameDeclaration
		layout     *codegen.GoTypePlan
		validation *codegen.ValidationPlan
	}
	consumerValidatorData struct {
		Name, Type, Body, Fmt string
		Nilable               bool
	}
)

// validator plans one typed check and reserves its declaration before visiting
// children, allowing recursive provider types to reuse the same validator.
func (p *consumerInputPlan) validator(attribute *expr.AttributeExpr, layout *codegen.GoTypePlan) (*consumerValidator, error) {
	key := consumerValidatorKey{attribute: attribute, declaration: layout.TypeDeclaration()}
	if existing := p.validators[key]; existing != nil {
		return existing, nil
	}
	name := codegen.NewPreferredName(codegen.NameFunction, "validateBoundValue", codegen.UnexportedName, specNameOrder{packagePath: p.packages.public.ImportPath(), key: fmt.Sprintf("bound-validator:%d", len(p.validatorOrder))})
	if err := p.packages.public.DeclareName(name); err != nil {
		return nil, err
	}
	planned := &consumerValidator{name: name, layout: layout}
	p.validators[key] = planned
	p.validatorOrder = append(p.validatorOrder, planned)
	validation, err := codegen.NewValidationPlan(attribute, layout, codegen.ValidationPlanOptions{Required: true, Alias: expr.IsAlias(attribute.Type), Bind: p.bindValidator})
	if err != nil {
		return nil, err
	}
	planned.validation = validation
	for _, imp := range validation.ImportPreferences() {
		if err := p.packages.fileImports.publicSpecs.Require(codegen.NewImport(imp.Name, imp.Path)); err != nil {
			return nil, err
		}
	}
	return planned, nil
}

// bindValidator supplies Goa with the generated function for a nested provider
// field. The same retained attribute and declaration share one private check.
func (p *consumerInputPlan) bindValidator(request codegen.ValidatorBindingRequest) (*codegen.NameDeclaration, error) {
	planned, err := p.validator(request.Attribute, request.Layout)
	if err != nil {
		return nil, err
	}
	return planned.name, nil
}

// validationSections links the typed checks after import names have been chosen.
func (p *consumerInputPlan) validationSections() ([]*codegen.SectionTemplate, error) {
	var sections []*codegen.SectionTemplate
	pkg := p.packages.public
	for _, planned := range p.validatorOrder {
		layout := planned.layout.Link(pkg.ImportPath(), pkg.ImportName)
		validation, err := planned.validation.Link(layout)
		if err != nil {
			return nil, err
		}
		sections = append(sections, &codegen.SectionTemplate{Name: "consumer-bound-validation", Source: consumerValidatorSource, Data: consumerValidatorData{
			Name: planned.name.Name(), Type: layout.Ref(), Body: validation.Render("value", "bound input"), Nilable: layout.ReferenceCanBeNil(), Fmt: pkg.ImportName("fmt"),
		}})
	}
	return sections, nil
}

const consumerValidatorSource = `
// {{ .Name }} checks an application-supplied value before encoding, returning
// its declared validation errors without changing the value.
func {{ .Name }}(value {{ .Type }}) (err error) {
    {{- if .Nilable }}
    if value == nil { return {{ .Fmt }}.Errorf("required bound input is nil") }
    {{- end }}
    {{ .Body }}
    return err
}
`
