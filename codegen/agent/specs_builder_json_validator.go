// Package codegen plans the raw JSON checks written into generated tool codecs.
// The planner turns each Goa type into direct Go calls so generated programs do
// not rebuild or walk a schema after they start.
package codegen

import (
	"fmt"
	"slices"
	"sort"

	"goa.design/goa-ai/boundedresult"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

const (
	jsonValidatorAny       = "any"
	jsonValidatorPrimitive = "primitive"
	jsonValidatorObject    = "object"
	jsonValidatorArray     = "array"
	jsonValidatorMap       = "map"
)

type (
	// jsonValidatorPlanner records every function before Goa fixes package names.
	// A named recursive type is recorded before its children so a child can call
	// the already planned function instead of making the generator recurse forever.
	jsonValidatorPlanner struct {
		plan      *toolSpecsPackagePlan
		key       string
		preferred string
		named     map[goaexpr.UserType]*plannedJSONValidator
	}
)

// declareJSONValidator plans one document validator and the value validators
// it calls. Bounded results add their generated bookkeeping fields to the root
// object without adding those fields to the public Go result.
func (p *toolSpecsPackagePlan) declareJSONValidator(key, preferred string, attribute *goaexpr.AttributeExpr, owner *contractTypeOwner, usage typeUsage) (*plannedJSONValidatorGraph, error) {
	document, err := p.declareJSONValidatorName(key, "validate"+preferred+"JSON", "document")
	if err != nil {
		return nil, err
	}
	planner := &jsonValidatorPlanner{
		plan:      p,
		key:       key,
		preferred: preferred,
		named:     make(map[goaexpr.UserType]*plannedJSONValidator),
	}
	root, err := planner.build(attribute, "Root", true)
	if err != nil {
		return nil, err
	}
	if usage == usageResult && owner.Bounds != nil && root.kind == jsonValidatorObject {
		if err := planner.addBoundedResultValidatorFields(root, owner.Bounds); err != nil {
			return nil, err
		}
	}
	return &plannedJSONValidatorGraph{
		document: document,
		root:     root,
	}, nil
}

// build plans one value validator and then its statically known children.
func (p *jsonValidatorPlanner) build(attribute *goaexpr.AttributeExpr, suffix string, root bool) (*plannedJSONValidator, error) {
	if primitive, ok := jsonValidatorPrimitiveType(attribute); ok {
		return p.primitiveValidator(primitive)
	}
	if goaexpr.IsUnion(attribute.Type) {
		return p.categoryValidator(jsonSchemaTypeObject)
	}
	if userType, ok := attribute.Type.(goaexpr.UserType); ok {
		identity := userType.Origin()
		if existing := p.named[identity]; existing != nil {
			return existing, nil
		}
		if !root {
			suffix = jsonValidatorUserTypeName(userType)
		}
		validator, err := p.newValidator(suffix, userType.Attribute(), root)
		if err != nil {
			return nil, err
		}
		p.named[identity] = validator
		if err := p.populate(validator, userType.Attribute(), suffix); err != nil {
			return nil, err
		}
		return validator, nil
	}
	validator, err := p.newValidator(suffix, attribute, root)
	if err != nil {
		return nil, err
	}
	if err := p.populate(validator, attribute, suffix); err != nil {
		return nil, err
	}
	return validator, nil
}

// newValidator reserves one private function name and records it before child
// planning. This order lets recursive children call the saved function.
func (p *jsonValidatorPlanner) newValidator(suffix string, attribute *goaexpr.AttributeExpr, root bool) (*plannedJSONValidator, error) {
	preferred := "validate" + p.preferred + goacodegen.Goify(suffix, true) + "JSONValue"
	if root {
		preferred = "validate" + p.preferred + "JSONValue"
	}
	declaration, err := p.plan.declareJSONValidatorName(
		p.key,
		preferred,
		fmt.Sprintf("value:%04d", len(p.plan.jsonValidators)),
	)
	if err != nil {
		return nil, err
	}
	validator := &plannedJSONValidator{
		declaration: declaration,
		expected:    generatedJSONType(attribute.Type),
	}
	p.plan.jsonValidators = append(p.plan.jsonValidators, validator)
	return validator, nil
}

// primitiveValidator returns the one package helper for an exact primitive Go
// representation. Callers still pass the actual field path and description.
func (p *jsonValidatorPlanner) primitiveValidator(primitive goaexpr.Primitive) (*plannedJSONValidator, error) {
	signed, unsigned, bits := jsonIntegerShape(primitive.Kind())
	key := jsonPrimitiveValidatorKey{
		expected:        generatedJSONType(primitive),
		signedInteger:   signed,
		unsignedInteger: unsigned,
		integerBits:     bits,
	}
	if existing := p.plan.jsonPrimitiveValidators[key]; existing != nil {
		return existing, nil
	}
	validator, err := p.newSharedValidator(jsonPrimitiveValidatorName(key), key.expected)
	if err != nil {
		return nil, err
	}
	if primitive.Kind() == goaexpr.AnyKind {
		validator.kind = jsonValidatorAny
	} else {
		validator.kind = jsonValidatorPrimitive
	}
	validator.expected = key.expected
	validator.signedInteger = signed
	validator.unsignedInteger = unsigned
	validator.integerBits = bits
	p.plan.jsonPrimitiveValidators[key] = validator
	return validator, nil
}

// categoryValidator returns one package helper that checks only a JSON value
// category. It is used where typed decoding owns all checks inside the value.
func (p *jsonValidatorPlanner) categoryValidator(expected string) (*plannedJSONValidator, error) {
	key := jsonPrimitiveValidatorKey{expected: expected}
	if existing := p.plan.jsonPrimitiveValidators[key]; existing != nil {
		return existing, nil
	}
	validator, err := p.newSharedValidator("validate"+goacodegen.Goify(expected, true)+"JSONValue", expected)
	if err != nil {
		return nil, err
	}
	validator.kind = jsonValidatorPrimitive
	validator.expected = expected
	p.plan.jsonPrimitiveValidators[key] = validator
	return validator, nil
}

// newSharedValidator reserves one package helper for a primitive check.
func (p *jsonValidatorPlanner) newSharedValidator(preferred, expected string) (*plannedJSONValidator, error) {
	declaration, err := p.plan.declareJSONValidatorName(
		p.key,
		preferred,
		fmt.Sprintf("value:%04d", len(p.plan.jsonValidators)),
	)
	if err != nil {
		return nil, err
	}
	validator := &plannedJSONValidator{
		declaration: declaration,
		expected:    expected,
	}
	p.plan.jsonValidators = append(p.plan.jsonValidators, validator)
	return validator, nil
}

// jsonValidatorPrimitiveType follows aliases and returns their concrete Goa
// primitive so the package can share the exact same raw JSON check.
func jsonValidatorPrimitiveType(attribute *goaexpr.AttributeExpr) (goaexpr.Primitive, bool) {
	if attribute == nil || attribute.Type == nil {
		return 0, false
	}
	switch actual := attribute.Type.(type) {
	case goaexpr.Primitive:
		return actual, true
	case goaexpr.UserType:
		return jsonValidatorPrimitiveType(actual.Attribute())
	default:
		return 0, false
	}
}

// jsonPrimitiveValidatorName returns the readable helper name for one exact
// primitive check.
func jsonPrimitiveValidatorName(key jsonPrimitiveValidatorKey) string {
	if key.expected == "" {
		return "validateAnyJSONValue"
	}
	if key.signedInteger || key.unsignedInteger {
		prefix := "Int"
		if key.unsignedInteger {
			prefix = "Uint"
		}
		if key.integerBits > 0 {
			prefix += fmt.Sprintf("%d", key.integerBits)
		}
		return "validate" + prefix + "JSONValue"
	}
	return "validate" + goacodegen.Goify(key.expected, true) + "JSONValue"
}

// populate records the direct calls needed for one known Goa value shape.
func (p *jsonValidatorPlanner) populate(validator *plannedJSONValidator, attribute *goaexpr.AttributeExpr, suffix string) error {
	switch actual := attribute.Type.(type) {
	case goaexpr.UserType:
		return p.populate(validator, actual.Attribute(), suffix)
	case *goaexpr.Object:
		validator.kind = jsonValidatorObject
		fields := slices.Clone(*actual)
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		validator.fields = make([]*plannedJSONValidatorField, 0, len(fields))
		for _, field := range fields {
			child, err := p.build(field.Attribute, suffix+goacodegen.Goify(field.Name, true), false)
			if err != nil {
				return err
			}
			validator.fields = append(validator.fields, &plannedJSONValidatorField{
				name: field.Name,
				call: &plannedJSONValidatorCall{validator: child, description: field.Attribute.Description},
			})
		}
	case *goaexpr.Array:
		validator.kind = jsonValidatorArray
		child, err := p.build(actual.ElemType, suffix+"Element", false)
		if err != nil {
			return err
		}
		validator.element = &plannedJSONValidatorCall{validator: child, inheritDescription: true}
	case *goaexpr.Map:
		validator.kind = jsonValidatorMap
		child, err := p.build(actual.ElemType, suffix+"Element", false)
		if err != nil {
			return err
		}
		validator.element = &plannedJSONValidatorCall{
			validator:          child,
			description:        actual.ElemType.Description,
			inheritDescription: actual.ElemType.Description == "",
		}
	default:
		return fmt.Errorf("plan JSON validator for unsupported Goa type %T", attribute.Type)
	}
	return nil
}

// jsonIntegerShape reports how generated code parses one Goa integer. A zero
// width means the generated program uses the width of its Go int or uint.
func jsonIntegerShape(kind goaexpr.Kind) (signed, unsigned bool, bits int) {
	switch kind {
	case goaexpr.IntKind:
		return true, false, 0
	case goaexpr.Int32Kind:
		return true, false, 32
	case goaexpr.Int64Kind:
		return true, false, 64
	case goaexpr.UIntKind:
		return false, true, 0
	case goaexpr.UInt32Kind:
		return false, true, 32
	case goaexpr.UInt64Kind:
		return false, true, 64
	case goaexpr.BooleanKind,
		goaexpr.Float32Kind,
		goaexpr.Float64Kind,
		goaexpr.StringKind,
		goaexpr.BytesKind,
		goaexpr.ArrayKind,
		goaexpr.ObjectKind,
		goaexpr.MapKind,
		goaexpr.UnionKind,
		goaexpr.UserTypeKind,
		goaexpr.ResultTypeKind,
		goaexpr.AnyKind:
		return false, false, 0
	}
	panic(fmt.Sprintf("unsupported Goa kind %d", kind))
}

// declareJSONValidatorName reserves one private validator function in the
// generated tool package.
func (p *toolSpecsPackagePlan) declareJSONValidatorName(key, preferred, role string) (*goacodegen.NameDeclaration, error) {
	declaration := goacodegen.NewPreferredName(
		goacodegen.NameFunction,
		preferred,
		goacodegen.UnexportedName,
		specNameOrder{packagePath: p.public.ImportPath(), key: key + ":json-validator:" + role},
	)
	if err := p.public.DeclareName(declaration); err != nil {
		return nil, err
	}
	return declaration, nil
}

// addBoundedResultValidatorFields checks the runtime-owned result details when
// they are present. The runtime adds required returned and truncated values
// from Bounds after encoding, so this decoder also accepts the semantic result
// before that projection happens.
func (p *jsonValidatorPlanner) addBoundedResultValidatorFields(root *plannedJSONValidator, bounds *ToolBoundsData) error {
	fields := []struct {
		name        string
		expected    string
		description string
	}{
		{
			name:        boundedresult.FieldReturned,
			expected:    jsonSchemaTypeInteger,
			description: "Number of items returned in this response after applying tool limits.",
		},
		{
			name:        boundedresult.FieldTotal,
			expected:    jsonSchemaTypeInteger,
			description: "Total number of matching items before truncation.",
		},
		{
			name:        boundedresult.FieldTruncated,
			expected:    "boolean",
			description: "True when this result is partial because tool limits or caps were applied.",
		},
		{
			name:        boundedresult.FieldRefinementHint,
			expected:    "string",
			description: "Short guidance on how to narrow the request when the result is truncated.",
		},
	}
	if cursor := modelJSONName(modelVisibleNextCursorField(bounds)); cursor != "" {
		fields = append(fields, struct {
			name        string
			expected    string
			description string
		}{
			name:        cursor,
			expected:    "string",
			description: "Continuation reference for the next page.",
		})
	}
	for _, field := range fields {
		var validator *plannedJSONValidator
		var err error
		if field.expected == jsonSchemaTypeInteger {
			validator, err = p.primitiveValidator(goaexpr.Int)
		} else {
			validator, err = p.categoryValidator(field.expected)
		}
		if err != nil {
			return err
		}
		planned := &plannedJSONValidatorField{
			name: field.name,
			call: &plannedJSONValidatorCall{
				validator:   validator,
				description: field.description,
			},
		}
		if index := slices.IndexFunc(root.fields, func(existing *plannedJSONValidatorField) bool {
			return existing.name == field.name
		}); index >= 0 {
			root.fields[index] = planned
		} else {
			root.fields = append(root.fields, planned)
		}
	}
	sort.Slice(root.fields, func(i, j int) bool { return root.fields[i].name < root.fields[j].name })
	return nil
}

// jsonValidatorUserTypeName returns a stable readable suffix for a named Goa type.
func jsonValidatorUserTypeName(userType goaexpr.UserType) string {
	switch actual := userType.(type) {
	case *goaexpr.UserTypeExpr:
		return actual.TypeName
	case *goaexpr.ResultTypeExpr:
		return actual.TypeName
	default:
		return userType.ID()
	}
}

// materializeJSONValidatorGraph resolves the two names owned by one codec.
func materializeJSONValidatorGraph(graph *plannedJSONValidatorGraph) (string, string) {
	return graph.document.Name(), graph.root.declaration.Name()
}

// materializeJSONValidators resolves every package value helper after Goa has
// selected all declaration and import names.
func materializeJSONValidators(plannedValidators []*plannedJSONValidator) []*jsonValidatorData {
	validators := make([]*jsonValidatorData, 0, len(plannedValidators))
	for _, planned := range plannedValidators {
		validator := &jsonValidatorData{
			Name:            planned.declaration.Name(),
			Kind:            planned.kind,
			Expected:        planned.expected,
			SignedInteger:   planned.signedInteger,
			UnsignedInteger: planned.unsignedInteger,
			IntegerBits:     planned.integerBits,
		}
		for _, field := range planned.fields {
			rendered := &jsonValidatorFieldData{
				Name: field.name,
			}
			if field.call != nil {
				rendered.Call = materializeJSONValidatorCall(field.call)
			}
			validator.Fields = append(validator.Fields, rendered)
		}
		if planned.element != nil {
			validator.Element = materializeJSONValidatorCall(planned.element)
		}
		validators = append(validators, validator)
	}
	return validators
}

// materializeJSONValidatorCall resolves the private function called for one child value.
func materializeJSONValidatorCall(call *plannedJSONValidatorCall) *jsonValidatorCallData {
	return &jsonValidatorCallData{
		Name:               call.validator.declaration.Name(),
		Description:        call.description,
		InheritDescription: call.inheritDescription,
	}
}
