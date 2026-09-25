// Package codec plans standalone values with existing transport and transform plans, with
// additional checks before any lossy JSON or Go conversion can happen.
package codec

import (
	"fmt"
	"strconv"

	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// standalonePlan retains schema checks and typed encode checks for one root.
	standalonePlan struct {
		root               *jsonshape.Node
		nodes              []*jsonshape.Node
		names              map[*jsonshape.Node]*codegen.NameDeclaration
		preflight          *codegen.NameDeclaration
		typedChecks        map[expr.UserType]*typedCheckPlan
		orderedTypedChecks []*typedCheckPlan
		originalValidator  *codegen.NameDeclaration
		originalValidators []*originalValidatorPlan
	}

	// typedCheckPlan retains one typed walk and its collection identity needs.
	typedCheckPlan struct {
		attribute           *expr.AttributeExpr
		name                *codegen.NameDeclaration
		recursiveCollection bool
	}

	// shapeData supplies the shared direct JSON validator template.
	shapeData struct {
		Name, Kind, Expected, TypeKey, ValueKey string
		SignedInteger, UnsignedInteger          bool
		IntegerBits                             int
		Fields, Branches                        []*shapeFieldData
		Element                                 *shapeCallData
	}

	// shapeFieldData binds one exact JSON name to its generated child check.
	shapeFieldData struct {
		Name string
		Call *shapeCallData
	}

	// shapeCallData retains tool-compatible error context without tool types.
	shapeCallData struct {
		Name, Description  string
		InheritDescription bool
		// AllowNull skips this child call only when its collection occurrence permits
		// nil elements in the generated Go representation.
		AllowNull bool
	}
)

// AddStandalone plans an always-strict, bidirectional value codec. Add retains
// its existing contract and output for protocol generators. The caller first
// checks SupportsStandalone, before creating this plan or reserving names.
func (p *Plan) AddStandalone(key, preferredName string, attribute *expr.AttributeExpr, layout *codegen.GoTypePlan) (*Value, error) {
	value, err := p.addOriginal(key, preferredName, attribute, layout)
	if err != nil {
		return nil, err
	}
	root, err := jsonshape.Build(attribute)
	if err != nil {
		return nil, err
	}
	plan := &standalonePlan{
		root: root, names: make(map[*jsonshape.Node]*codegen.NameDeclaration),
		typedChecks: make(map[expr.UserType]*typedCheckPlan),
	}
	value.standalone = plan
	if err := p.planJSONHelpers(); err != nil {
		return nil, err
	}
	if err := value.planOriginalValidation(layout); err != nil {
		return nil, fmt.Errorf("standalone JSON codec %q original validation: %w", key, err)
	}
	if err := value.planShape(root); err != nil {
		return nil, err
	}
	plan.preflight, err = value.strictName("check"+preferredName+"Value", "preflight")
	if err != nil {
		return nil, err
	}
	if err := walkAttribute(attribute, make(map[expr.UserType]struct{}), func(current *expr.AttributeExpr) error {
		named, ok := current.Type.(expr.UserType)
		if !ok || named == expr.Empty || plan.typedChecks[named.Origin()] != nil {
			return nil
		}
		name, err := value.strictName("check"+preferredName+named.Name()+"Value", "typed:"+strconv.Itoa(len(plan.orderedTypedChecks)))
		if err != nil {
			return err
		}
		check := &typedCheckPlan{attribute: current, name: name}
		if expr.IsArray(named) || expr.IsMap(named) {
			if err := walkAttribute(named.Attribute(), make(map[expr.UserType]struct{}), func(child *expr.AttributeExpr) error {
				if nested, ok := child.Type.(expr.UserType); ok && nested.Origin() == named.Origin() {
					check.recursiveCollection = true
				}
				return nil
			}); err != nil {
				return err
			}
			if check.recursiveCollection {
				if err := p.requireImport(codegen.NewImport("reflect", "reflect")); err != nil {
					return err
				}
			}
		}
		plan.typedChecks[named.Origin()] = check
		plan.orderedTypedChecks = append(plan.orderedTypedChecks, check)
		return nil
	}); err != nil {
		return nil, err
	}
	for _, imp := range []string{"sort", "strconv", "unicode/utf8"} {
		name := imp
		if imp == "unicode/utf8" {
			name = "utf8"
		}
		if err := p.requireImport(codegen.NewImport(name, imp)); err != nil {
			return nil, err
		}
	}
	return value, nil
}

// strictName reserves an implementation name without changing existing plans.
func (v *Value) strictName(preferred, role string) (*codegen.NameDeclaration, error) {
	name := codegen.NewPreferredName(codegen.NameFunction, preferred, codegen.UnexportedName,
		nameOrder{packagePath: v.plan.pkg.ImportPath(), key: v.key + ":strict:" + role})
	if err := v.plan.pkg.DeclareName(name); err != nil {
		return nil, err
	}
	return name, nil
}

// planShape records a node before its children to terminate recursive graphs.
func (v *Value) planShape(node *jsonshape.Node) error {
	if v.standalone.names[node] != nil {
		return nil
	}
	name, err := v.strictName("validate"+v.preferredName+"JSONValue", "shape:"+strconv.Itoa(len(v.standalone.nodes)))
	if err != nil {
		return err
	}
	v.standalone.names[node] = name
	v.standalone.nodes = append(v.standalone.nodes, node)
	for _, fields := range [][]*jsonshape.Field{node.Fields, node.Branches} {
		for _, field := range fields {
			if err := v.planShape(field.Node); err != nil {
				return err
			}
		}
	}
	if node.Element != nil {
		return v.planShape(node.Element)
	}
	return nil
}

// shapeData links names for the same shared rendering path used by tool codecs.
func (v *Value) shapeData() []*shapeData {
	data := make([]*shapeData, 0, len(v.standalone.nodes))
	for _, node := range v.standalone.nodes {
		shape := &shapeData{Name: v.standalone.names[node].Name(), Kind: node.Kind, Expected: node.Expected}
		if node.Kind == "primitive" {
			shape.SignedInteger, shape.UnsignedInteger, shape.IntegerBits = jsonshape.IntegerShape(node.Primitive.Kind())
		}
		for _, field := range node.Fields {
			shape.Fields = append(shape.Fields, &shapeFieldData{Name: field.Name,
				Call: &shapeCallData{Name: v.standalone.names[field.Node].Name(), Description: field.Description}})
		}
		if node.Element != nil {
			shape.Element = &shapeCallData{Name: v.standalone.names[node.Element].Name(), InheritDescription: true}
			if array := expr.AsArray(node.Attribute.Type); array != nil {
				shape.Element.AllowNull = !array.NonNullableElems && codegen.IsNilable(array.ElemType.Type)
			} else if mapping := expr.AsMap(node.Attribute.Type); mapping != nil {
				shape.Element.AllowNull = codegen.IsNilable(mapping.ElemType.Type)
			}
		}
		if node.Union != nil {
			shape.TypeKey, shape.ValueKey = node.Union.GetTypeKey(), node.Union.GetValueKey()
			for _, branch := range node.Branches {
				shape.Branches = append(shape.Branches, &shapeFieldData{Name: branch.Name,
					Call: &shapeCallData{Name: v.standalone.names[branch.Node].Name(), Description: branch.Description}})
			}
		}
		data = append(data, shape)
	}
	return data
}
