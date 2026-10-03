// Package jsonshape plans direct JSON shape checks from Goa expressions. It
// shares schema traversal between generators; generated programs do not
// interpret schemas. Named recursive values retain their original identity.
package jsonshape

import (
	"fmt"
	"slices"
	"sort"

	"goa.design/goa/v3/expr"
)

type (
	// Node is one statically known JSON value in a planned graph.
	Node struct {
		// Attribute is the original expression, including validation metadata.
		Attribute *expr.AttributeExpr
		// UserType is the original named declaration, if this node has one.
		UserType expr.UserType
		// Kind is any, primitive, object, array, map, or union.
		Kind string
		// Expected is the corresponding JSON category.
		Expected string
		// Primitive records the precise Goa primitive, including integer width.
		Primitive expr.Primitive
		// Fields are object members in deterministic name order.
		Fields []*Field
		// Element is the array or map value shape.
		Element *Node
		// ElementDescription retains a map element occurrence's description.
		ElementDescription string
		// Union is the authored discriminator/value contract.
		Union *expr.Union
		// Branches are the union alternatives in declaration order.
		Branches []*Field
	}

	// Field associates a design name and description with a child shape.
	Field struct {
		// Name is the exact authored JSON name.
		Name string
		// Description is the authored field description.
		Description string
		// Node is the statically planned child.
		Node *Node
	}

	// builder records nodes before their children, so recursive graphs terminate.
	builder struct {
		named  map[expr.UserType]*Node
		unions map[*expr.Union]*Node
	}
)

// Build plans one graph without changing the supplied expressions.
func Build(attribute *expr.AttributeExpr) (*Node, error) {
	b := &builder{
		named:  make(map[expr.UserType]*Node),
		unions: make(map[*expr.Union]*Node),
	}
	return b.build(attribute)
}

// build preserves named identity and union sharing before following children.
func (b *builder) build(attribute *expr.AttributeExpr) (*Node, error) {
	if primitive, ok := PrimitiveType(attribute); ok {
		kind := "primitive"
		if primitive == expr.Any {
			kind = "any"
		}
		return &Node{Attribute: attribute, Kind: kind, Expected: Category(primitive), Primitive: primitive}, nil
	}
	if userType, ok := attribute.Type.(expr.UserType); ok {
		if node := b.named[userType.Origin()]; node != nil {
			return node, nil
		}
		node := &Node{Attribute: userType.Attribute(), UserType: userType}
		b.named[userType.Origin()] = node
		if err := b.populate(node, userType.Attribute()); err != nil {
			return nil, err
		}
		return node, nil
	}
	if union, ok := attribute.Type.(*expr.Union); ok {
		if node := b.unions[union]; node != nil {
			return node, nil
		}
		node := &Node{Attribute: attribute}
		b.unions[union] = node
		if err := b.populate(node, attribute); err != nil {
			return nil, err
		}
		return node, nil
	}
	node := &Node{Attribute: attribute}
	if err := b.populate(node, attribute); err != nil {
		return nil, err
	}
	return node, nil
}

// populate records direct calls to statically known children.
func (b *builder) populate(node *Node, attribute *expr.AttributeExpr) error {
	node.Expected = Category(attribute.Type)
	switch actual := attribute.Type.(type) {
	case expr.UserType:
		return b.populate(node, actual.Attribute())
	case *expr.Object:
		node.Kind = "object"
		fields := slices.Clone(*actual)
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		for _, field := range fields {
			child, err := b.build(field.Attribute)
			if err != nil {
				return err
			}
			node.Fields = append(node.Fields, &Field{Name: field.Name, Description: field.Attribute.Description, Node: child})
		}
	case *expr.Array:
		node.Kind = "array"
		child, err := b.build(actual.ElemType)
		if err != nil {
			return err
		}
		node.Element = child
	case *expr.Map:
		node.Kind = "map"
		node.ElementDescription = actual.ElemType.Description
		child, err := b.build(actual.ElemType)
		if err != nil {
			return err
		}
		node.Element = child
	case *expr.Union:
		node.Kind, node.Union = "union", actual
		for _, branch := range actual.Values {
			child, err := b.build(branch.Attribute)
			if err != nil {
				return err
			}
			node.Branches = append(node.Branches, &Field{Name: branch.Name, Description: branch.Attribute.Description, Node: child})
		}
	default:
		return fmt.Errorf("plan JSON validator for unsupported Goa type %T", attribute.Type)
	}
	return nil
}

// PrimitiveType follows aliases to their concrete Goa primitive.
func PrimitiveType(attribute *expr.AttributeExpr) (expr.Primitive, bool) {
	if attribute == nil || attribute.Type == nil {
		return 0, false
	}
	switch actual := attribute.Type.(type) {
	case expr.Primitive:
		return actual, true
	case expr.UserType:
		return PrimitiveType(actual.Attribute())
	default:
		return 0, false
	}
}

// Category returns the exact JSON category; Any has no fixed category.
func Category(dt expr.DataType) string {
	switch actual := dt.(type) {
	case expr.UserType:
		return Category(actual.Attribute().Type)
	case *expr.Object, *expr.Map, *expr.Union:
		return "object"
	case *expr.Array:
		return "array"
	case expr.Primitive:
		switch actual.Kind() {
		case expr.BooleanKind:
			return "boolean"
		case expr.StringKind, expr.BytesKind:
			return "string"
		case expr.IntKind, expr.Int32Kind, expr.Int64Kind, expr.UIntKind, expr.UInt32Kind, expr.UInt64Kind:
			return "integer"
		case expr.Float32Kind, expr.Float64Kind:
			return "number"
		case expr.ArrayKind, expr.ObjectKind, expr.MapKind, expr.UnionKind, expr.UserTypeKind, expr.ResultTypeKind, expr.AnyKind:
			return ""
		}
	}
	return ""
}

// IntegerShape reports the declared signedness and width. Zero means Go int/uint.
func IntegerShape(kind expr.Kind) (signed, unsigned bool, bits int) {
	switch kind {
	case expr.IntKind:
		return true, false, 0
	case expr.Int32Kind:
		return true, false, 32
	case expr.Int64Kind:
		return true, false, 64
	case expr.UIntKind:
		return false, true, 0
	case expr.UInt32Kind:
		return false, true, 32
	case expr.UInt64Kind:
		return false, true, 64
	case expr.BooleanKind, expr.Float32Kind, expr.Float64Kind, expr.StringKind, expr.BytesKind,
		expr.ArrayKind, expr.ObjectKind, expr.MapKind, expr.UnionKind, expr.UserTypeKind, expr.ResultTypeKind, expr.AnyKind:
		return false, false, 0
	}
	panic(fmt.Sprintf("unsupported Goa kind %d", kind))
}
