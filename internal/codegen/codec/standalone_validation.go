// Package codec validates original and decoded transport values before conversion
// can erase required nil fields. Goa owns the checks and layouts; this file owns
// the private function declarations, their planning lifetime and their output.
package codec

import (
	"fmt"
	"slices"
	"strconv"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// originalValidatorKey groups uses of one named definition and Go layout.
	// Each group retains separate helpers for different effective rules.
	originalValidatorKey struct {
		definition  *expr.AttributeExpr
		declaration *codegen.TypeDeclaration
		policy      codegen.GoLayoutPolicy
	}

	// originalValidatorPlan uses one retained type for its parameter and checks.
	originalValidatorPlan struct {
		layout      *codegen.GoTypePlan
		contents    *codegen.GoTypePlan
		rules       *expr.ValidationExpr
		declaration *codegen.NameDeclaration
		validation  *codegen.ValidationPlan
	}

	// originalValidatorPlanner shares helpers within one output package.
	// Recursive requests reuse a declaration reserved before its body is read.
	originalValidatorPlanner struct {
		value       *Value
		definitions map[originalValidatorKey][]*originalValidatorPlan
	}

	// originalValidatorData contains only linked names and retained Go checks.
	originalValidatorData struct {
		Name, Reference, Validation string
		Pointer                     bool
	}
)

// planOriginalValidation checks the supplied root occurrence in both retained
// layouts. Encode checks original values before conversion; decode checks the
// transport value while required field presence is still available.
func (v *Value) planOriginalValidation(layout *codegen.GoTypePlan) error {
	var planner *originalValidatorPlanner
	for _, value := range v.plan.values {
		if value.standalone != nil && value.standalone.originalPlanner != nil {
			planner = value.standalone.originalPlanner
			break
		}
	}
	if planner == nil {
		planner = &originalValidatorPlanner{
			value:       v,
			definitions: make(map[originalValidatorKey][]*originalValidatorPlan),
		}
	}
	v.standalone.originalPlanner = planner
	if codegen.NeedsValidation(v.service, layout.Policy()) {
		root, err := planner.add(v.service, layout)
		if err != nil {
			return err
		}
		v.standalone.originalValidator = root.declaration
	}
	// A root with the named definition's effective rules uses its existing
	// transport validator. Only different rules need a separate function.
	rules := expr.EffectiveValidation(v.transport)
	definition := &expr.AttributeExpr{Type: v.transport.Type}
	if sameValidationRules(rules, expr.EffectiveValidation(definition)) {
		return nil
	}
	for _, value := range v.plan.values {
		if value != v && value.standalone != nil && value.types[0] == v.types[0] &&
			sameValidationRules(rules, expr.EffectiveValidation(value.transport)) {
			v.standalone.transportValidator = value.standalone.transportValidator
			return nil
		}
	}
	declaration, err := v.strictName("validate"+v.preferredName+"JSON", "occurrence")
	if err != nil {
		return err
	}
	transportLayout := v.transportLayout
	if expr.IsPrimitive(v.transport.Type) {
		// A scalar root is a value parameter. Pointer presence belongs only to
		// object fields, as it does in the shared named transport validators.
		policy := transportLayout.Policy()
		policy.Pointer = false
		transportLayout, err = codegen.PlanGoType(v.transport, codegen.GoTypePlanOptions{
			Owner: v.plan.pkg.ImportPath(), Policy: policy, RetainNamedValue: true,
			Bind: func(request codegen.GoTypeBindingRequest) (codegen.GoTypeBinding, error) {
				userType := request.Attribute.Type.(expr.UserType)
				named := v.plan.originals.typesByLocal[userType.Origin()]
				if named == nil {
					return codegen.GoTypeBinding{}, fmt.Errorf("transport type %q has no declaration", userType.Name())
				}
				return codegen.GoTypeBinding{Owner: v.plan.pkg.ImportPath(), Declaration: named.declaration}, nil
			},
		})
		if err != nil {
			return err
		}
	}
	validation, err := codegen.NewValidationPlan(v.transport, transportLayout, codegen.ValidationPlanOptions{
		Required: true,
		Alias:    expr.IsAlias(v.transport.Type),
		Bind: func(request codegen.ValidatorBindingRequest) (*codegen.NameDeclaration, error) {
			userType := request.Attribute.Type.(expr.UserType)
			for _, nested := range v.types {
				if nested.userType.Origin() == userType.Origin() {
					return nested.validatorDeclaration, nil
				}
			}
			return nil, fmt.Errorf("transport validator for %q has no declaration", userType.Name())
		},
	})
	if err != nil {
		return err
	}
	for _, imp := range validation.ImportPreferences() {
		if err := v.plan.requireImport(codegen.NewImport(imp.Name, imp.Path)); err != nil {
			return err
		}
	}
	v.standalone.transportValidator = declaration
	v.standalone.originalValidators = append(v.standalone.originalValidators, &originalValidatorPlan{
		layout: transportLayout, declaration: declaration, validation: validation,
	})
	return nil
}

// add predeclares one private helper before Goa requests nested helpers. The
// enclosing occurrence keeps requiredness and element-nullability checks.
func (p *originalValidatorPlanner) add(attribute *expr.AttributeExpr, layout *codegen.GoTypePlan) (*originalValidatorPlan, error) {
	userType := attribute.Type.(expr.UserType)
	key := originalValidatorKey{userType.Attribute(), layout.TypeDeclaration(), layout.Policy()}
	contents, err := originalDefinitionLayout(layout, userType.Attribute())
	if err != nil {
		return nil, err
	}
	rules := expr.EffectiveValidation(attribute)
	for _, planned := range p.definitions[key] {
		if contents.Equivalent(planned.contents) && sameValidationRules(rules, planned.rules) {
			return planned, nil
		}
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
	planned := &originalValidatorPlan{
		layout: layout, contents: contents, rules: rules, declaration: declaration,
	}
	p.definitions[key] = append(p.definitions[key], planned)
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

// sameValidationRules compares Goa's merged rules before sharing a helper.
// Enum literals use the same Go constant spelling as Goa's validation templates;
// nil and empty enum lists stay distinct because they emit different checks.
func sameValidationRules(left, right *expr.ValidationExpr) bool {
	if left == nil {
		left = &expr.ValidationExpr{}
	}
	if right == nil {
		right = &expr.ValidationExpr{}
	}
	return fmt.Sprintf("%#v", left.Values) == fmt.Sprintf("%#v", right.Values) &&
		left.Format == right.Format && left.Pattern == right.Pattern &&
		sameValidationBound(left.ExclusiveMinimum, right.ExclusiveMinimum) &&
		sameValidationBound(left.Minimum, right.Minimum) &&
		sameValidationBound(left.ExclusiveMaximum, right.ExclusiveMaximum) &&
		sameValidationBound(left.Maximum, right.Maximum) &&
		sameValidationBound(left.MinLength, right.MinLength) &&
		sameValidationBound(left.MaxLength, right.MaxLength) &&
		slices.Equal(left.Required, right.Required)
}

// sameValidationBound preserves both absence and the inclusive or exclusive
// bound value when deciding whether two generated checks may share a function.
func sameValidationBound[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
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
			Pointer:    planned.layout.ReferenceIsPointer(),
		})
	}
	return result, nil
}
