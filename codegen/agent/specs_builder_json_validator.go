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
		planner.addBoundedResultValidatorFields(root, owner.Bounds)
	}
	graph := &plannedJSONValidatorGraph{
		key:       key,
		preferred: "validate" + preferred + "JSON",
		role:      "document",
		root:      root,
	}
	p.jsonValidatorGraphs = append(p.jsonValidatorGraphs, graph)
	return graph, nil
}

// build plans one value validator and then its statically known children.
func (p *jsonValidatorPlanner) build(attribute *goaexpr.AttributeExpr, suffix string, root bool) (*plannedJSONValidator, error) {
	if primitive, ok := jsonValidatorPrimitiveType(attribute); ok {
		return p.primitiveValidator(primitive), nil
	}
	if goaexpr.IsUnion(attribute.Type) {
		return p.categoryValidator(jsonSchemaTypeObject), nil
	}
	if userType, ok := attribute.Type.(goaexpr.UserType); ok {
		identity := userType.Origin()
		if existing := p.named[identity]; existing != nil {
			return existing, nil
		}
		if !root {
			suffix = jsonValidatorUserTypeName(userType)
		}
		validator := p.newValidator(suffix, userType.Attribute(), root)
		p.named[identity] = validator
		if err := p.populate(validator, userType.Attribute(), suffix); err != nil {
			return nil, err
		}
		return validator, nil
	}
	validator := p.newValidator(suffix, attribute, root)
	if err := p.populate(validator, attribute, suffix); err != nil {
		return nil, err
	}
	return validator, nil
}

// newValidator records a value check before planning its children. This order
// lets recursive children call the saved function.
func (p *jsonValidatorPlanner) newValidator(suffix string, attribute *goaexpr.AttributeExpr, root bool) *plannedJSONValidator {
	preferred := "validate" + p.preferred + goacodegen.Goify(suffix, true) + "JSONValue"
	if root {
		preferred = "validate" + p.preferred + "JSONValue"
	}
	validator := &plannedJSONValidator{
		key:       p.key,
		preferred: preferred,
		role:      fmt.Sprintf("value:%04d", len(p.plan.jsonValidators)),
		expected:  generatedJSONType(attribute.Type),
	}
	p.plan.jsonValidators = append(p.plan.jsonValidators, validator)
	return validator
}

// primitiveValidator plans the exact JSON check for one primitive Go value.
// The package pass later shares it with every matching check.
func (p *jsonValidatorPlanner) primitiveValidator(primitive goaexpr.Primitive) *plannedJSONValidator {
	signed, unsigned, bits := jsonIntegerShape(primitive.Kind())
	expected := generatedJSONType(primitive)
	validator := p.newPrimitiveValidator(jsonPrimitiveValidatorName(expected, signed, unsigned, bits), expected)
	if primitive.Kind() == goaexpr.AnyKind {
		validator.kind = jsonValidatorAny
	} else {
		validator.kind = jsonValidatorPrimitive
	}
	validator.signedInteger = signed
	validator.unsignedInteger = unsigned
	validator.integerBits = bits
	return validator
}

// categoryValidator plans a JSON category check where typed decoding checks
// the contents of the value.
func (p *jsonValidatorPlanner) categoryValidator(expected string) *plannedJSONValidator {
	validator := p.newPrimitiveValidator("validate"+goacodegen.Goify(expected, true)+"JSONValue", expected)
	validator.kind = jsonValidatorPrimitive
	return validator
}

// newPrimitiveValidator records a primitive check before package sharing.
func (p *jsonValidatorPlanner) newPrimitiveValidator(preferred, expected string) *plannedJSONValidator {
	validator := &plannedJSONValidator{
		key:       p.key,
		preferred: preferred,
		role:      fmt.Sprintf("value:%04d", len(p.plan.jsonValidators)),
		expected:  expected,
	}
	p.plan.jsonValidators = append(p.plan.jsonValidators, validator)
	return validator
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
func jsonPrimitiveValidatorName(expected string, signed, unsigned bool, bits int) string {
	if expected == "" {
		return "validateAnyJSONValue"
	}
	if signed || unsigned {
		prefix := "Int"
		if unsigned {
			prefix = "Uint"
		}
		if bits > 0 {
			prefix += fmt.Sprintf("%d", bits)
		}
		return "validate" + prefix + "JSONValue"
	}
	return "validate" + goacodegen.Goify(expected, true) + "JSONValue"
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
func (p *jsonValidatorPlanner) addBoundedResultValidatorFields(root *plannedJSONValidator, bounds *ToolBoundsData) {
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
		if field.expected == jsonSchemaTypeInteger {
			validator = p.primitiveValidator(goaexpr.Int)
		} else {
			validator = p.categoryValidator(field.expected)
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

// finalizeJSONValidators keeps only checks called by generated codecs and
// shares equal checks after every codec in the package has finished planning.
// It then reserves names only for the functions that the generated file writes.
func (p *toolSpecsPackagePlan) finalizeJSONValidators() error {
	graphs := renderedJSONValidatorGraphs(p.jsonValidatorGraphs)
	reachable := reachableJSONValidators(graphs)
	validators := make([]*plannedJSONValidator, 0, len(reachable))
	for _, validator := range p.jsonValidators {
		if reachable[validator] {
			validators = append(validators, validator)
		}
	}
	groups := jsonValidatorGroups(validators)
	canonicalByGroup := make([]*plannedJSONValidator, len(validators))
	canonical := make(map[*plannedJSONValidator]*plannedJSONValidator, len(validators))
	kept := make([]*plannedJSONValidator, 0, len(validators))
	for _, validator := range validators {
		group := groups[validator]
		shared := canonicalByGroup[group]
		if shared == nil {
			var err error
			validator.declaration, err = p.declareJSONValidatorName(
				validator.key,
				validator.preferred,
				validator.role,
			)
			if err != nil {
				return err
			}
			shared = validator
			canonicalByGroup[group] = validator
			kept = append(kept, validator)
		}
		canonical[validator] = shared
	}
	for _, validator := range kept {
		for _, field := range validator.fields {
			if field.call != nil {
				field.call.validator = canonical[field.call.validator]
			}
		}
		if validator.element != nil {
			validator.element.validator = canonical[validator.element.validator]
		}
	}
	for _, graph := range graphs {
		graph.root = canonical[graph.root]
	}
	documents := make(map[*plannedJSONValidator]*goacodegen.NameDeclaration, len(graphs))
	documentValidators := make([]*plannedJSONValidatorGraph, 0, len(graphs))
	for _, graph := range graphs {
		if document := documents[graph.root]; document != nil {
			graph.document = document
			continue
		}
		document, err := p.declareJSONValidatorName(graph.key, graph.preferred, graph.role)
		if err != nil {
			return err
		}
		graph.document = document
		documents[graph.root] = document
		documentValidators = append(documentValidators, graph)
	}
	p.jsonDocumentValidators = documentValidators
	p.jsonValidators = kept
	return nil
}

// renderedJSONValidatorGraphs returns document checks called by generated
// codecs in the same order that their contracts were planned.
func renderedJSONValidatorGraphs(graphs []*plannedJSONValidatorGraph) []*plannedJSONValidatorGraph {
	rendered := make([]*plannedJSONValidatorGraph, 0, len(graphs))
	for _, graph := range graphs {
		if graph.render {
			rendered = append(rendered, graph)
		}
	}
	return rendered
}

// reachableJSONValidators finds every value check called by a rendered
// document reader, including calls that lead back to a recursive parent.
func reachableJSONValidators(graphs []*plannedJSONValidatorGraph) map[*plannedJSONValidator]bool {
	reachable := make(map[*plannedJSONValidator]bool)
	remaining := make([]*plannedJSONValidator, 0, len(graphs))
	for _, graph := range graphs {
		remaining = append(remaining, graph.root)
	}
	for len(remaining) > 0 {
		last := len(remaining) - 1
		validator := remaining[last]
		remaining = remaining[:last]
		if reachable[validator] {
			continue
		}
		reachable[validator] = true
		for _, field := range validator.fields {
			if field.call != nil {
				remaining = append(remaining, field.call.validator)
			}
		}
		if validator.element != nil {
			remaining = append(remaining, validator.element.validator)
		}
	}
	return reachable
}

// jsonValidatorGroups gives equal recursive checks the same number. Each pass
// compares child group numbers from the previous pass. The numbers stop
// changing when the complete recursive structures have been compared.
func jsonValidatorGroups(validators []*plannedJSONValidator) map[*plannedJSONValidator]int {
	groups := make(map[*plannedJSONValidator]int, len(validators))
	for {
		next := make(map[*plannedJSONValidator]int, len(validators))
		representatives := make([]*plannedJSONValidator, 0, len(validators))
		for _, validator := range validators {
			group := -1
			for index, representative := range representatives {
				if sameJSONValidator(validator, representative, groups) {
					group = index
					break
				}
			}
			if group == -1 {
				group = len(representatives)
				representatives = append(representatives, validator)
			}
			next[validator] = group
		}
		if sameJSONValidatorGroups(validators, groups, next) {
			return next
		}
		groups = next
	}
}

// sameJSONValidator compares everything one generated value-checking function
// does. Recursive calls are compared through their current group numbers.
func sameJSONValidator(left, right *plannedJSONValidator, groups map[*plannedJSONValidator]int) bool {
	if left.kind != right.kind ||
		left.expected != right.expected ||
		left.signedInteger != right.signedInteger ||
		left.unsignedInteger != right.unsignedInteger ||
		left.integerBits != right.integerBits ||
		len(left.fields) != len(right.fields) ||
		!sameJSONValidatorCall(left.element, right.element, groups) {
		return false
	}
	for index, leftField := range left.fields {
		rightField := right.fields[index]
		if leftField.name != rightField.name || !sameJSONValidatorCall(leftField.call, rightField.call, groups) {
			return false
		}
	}
	return true
}

// sameJSONValidatorCall compares the child function and the description sent
// to it in generated code.
func sameJSONValidatorCall(left, right *plannedJSONValidatorCall, groups map[*plannedJSONValidator]int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.description == right.description &&
		left.inheritDescription == right.inheritDescription &&
		groups[left.validator] == groups[right.validator]
}

// sameJSONValidatorGroups reports whether another comparison pass would keep
// every function in the same group.
func sameJSONValidatorGroups(validators []*plannedJSONValidator, left, right map[*plannedJSONValidator]int) bool {
	for _, validator := range validators {
		if left[validator] != right[validator] {
			return false
		}
	}
	return true
}

// materializeJSONValidatorGraph resolves the two names owned by one codec.
func materializeJSONValidatorGraph(graph *plannedJSONValidatorGraph) (string, string) {
	return graph.document.Name(), graph.root.declaration.Name()
}

// materializeJSONDocumentValidators resolves each package document reader and
// the value check it calls after Goa has selected all declaration names.
func materializeJSONDocumentValidators(graphs []*plannedJSONValidatorGraph) []*jsonDocumentValidatorData {
	validators := make([]*jsonDocumentValidatorData, 0, len(graphs))
	for _, graph := range graphs {
		validators = append(validators, &jsonDocumentValidatorData{
			Name: graph.document.Name(),
			Root: graph.root.declaration.Name(),
		})
	}
	return validators
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
