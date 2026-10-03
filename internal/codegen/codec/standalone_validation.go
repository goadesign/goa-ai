// Package codec validates selected original Go values before conversion can erase
// required nil fields. Goa owns the checks and layouts; this file owns only the
// private function declarations, their planning lifetime and their output.
package codec

import (
	"strconv"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// originalValidatorKey distinguishes definitions that share an origin but
	// have different declarations or field representations.
	originalValidatorKey struct {
		definition  *expr.AttributeExpr
		declaration *codegen.TypeDeclaration
		policy      codegen.GoLayoutPolicy
	}

	// originalValidatorPlan uses one retained type for its parameter and checks.
	originalValidatorPlan struct {
		layout      *codegen.GoTypePlan
		declaration *codegen.NameDeclaration
		validation  *codegen.ValidationPlan
	}

	// originalValidatorPlanner lives only while one selected value is planned.
	// Recursive requests reuse a declaration reserved before its body is read.
	originalValidatorPlanner struct {
		value       *Value
		definitions map[originalValidatorKey]*originalValidatorPlan
	}

	// originalValidatorData contains only linked names and retained Go checks.
	originalValidatorData struct {
		Name, Reference, Validation string
	}
)

// planOriginalValidation uses the service's retained type layout. Empty
// definitions need no helper; the existing preflight still checks the root.
func (v *Value) planOriginalValidation(layout *codegen.GoTypePlan) error {
	userType := v.service.Type.(expr.UserType)
	if !codegen.NeedsValidation(userType.Attribute(), layout.Policy()) {
		return nil
	}
	planner := originalValidatorPlanner{
		value:       v,
		definitions: make(map[originalValidatorKey]*originalValidatorPlan),
	}
	root, err := planner.add(v.service, layout)
	if err != nil {
		return err
	}
	v.standalone.originalValidator = root.declaration
	return nil
}

// add predeclares one private helper before Goa requests nested helpers. The
// enclosing occurrence keeps requiredness and element-nullability checks.
func (p *originalValidatorPlanner) add(attribute *expr.AttributeExpr, layout *codegen.GoTypePlan) (*originalValidatorPlan, error) {
	userType := attribute.Type.(expr.UserType)
	key := originalValidatorKey{userType.Attribute(), layout.TypeDeclaration(), layout.Policy()}
	if planned := p.definitions[key]; planned != nil {
		return planned, nil
	}
	pkg := p.value.plan.pkg
	declaration, err := pkg.DeclareDependentName(codegen.NameFunction, layout.TypeDeclaration().Declaration(),
		"validate", "Original", nameOrder{
			packagePath: pkg.ImportPath(),
			key:         p.value.key + ":original:" + strconv.Itoa(len(p.value.standalone.originalValidators)),
		})
	if err != nil {
		return nil, err
	}
	planned := &originalValidatorPlan{layout: layout, declaration: declaration}
	p.definitions[key] = planned
	p.value.standalone.originalValidators = append(p.value.standalone.originalValidators, planned)
	planned.validation, err = codegen.NewValidationPlan(attribute, layout, codegen.ValidationPlanOptions{
		Required: true,
		Alias:    expr.IsAlias(userType),
		Bind: func(request codegen.ValidatorBindingRequest) (*codegen.NameDeclaration, error) {
			child, err := p.add(request.Attribute, request.Layout)
			if err != nil {
				return nil, err
			}
			return child.declaration, nil
		},
	})
	if err != nil {
		return nil, err
	}
	for _, imp := range layout.ImportPreferences() {
		if imp.Path == pkg.ImportPath() {
			continue
		}
		if err := pkg.ReserveGeneratedImport(codegen.NewImport(imp.Name, imp.Path)); err != nil {
			return nil, err
		}
		p.value.plan.locatedImportPaths[imp.Path] = struct{}{}
	}
	for _, imp := range planned.validation.ImportPreferences() {
		if imp.Path == pkg.ImportPath() {
			continue
		}
		if err := p.value.plan.requireImport(codegen.NewImport(imp.Name, imp.Path)); err != nil {
			return nil, err
		}
	}
	return planned, nil
}

// originalValidationData links only the plans retained before freeze. It does
// not search expressions again to choose fields, types or validation rules.
func (v *Value) originalValidationData() ([]*originalValidatorData, error) {
	pkg := v.plan.pkg
	var result []*originalValidatorData
	for _, planned := range v.standalone.originalValidators {
		parameter := planned.layout.Link(pkg.ImportPath(), pkg.ImportName)
		body, err := planned.validation.Link(parameter)
		if err != nil {
			return nil, err
		}
		result = append(result, &originalValidatorData{
			Name:       planned.declaration.Name(),
			Reference:  parameter.Ref(),
			Validation: body.Render("value", "value"),
		})
	}
	return result, nil
}
