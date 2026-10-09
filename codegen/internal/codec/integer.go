// Package codec gives private integer fields the JSON number contract declared
// by their schema. The generated decoder accepts equivalent decimal and exponent
// spellings, then checks the authored Go integer range without using floats.
package codec

import (
	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// integerTransportWalker records the private integer types added to a copied
	// JSON graph, without changing the service graph or revisiting named cycles.
	integerTransportWalker struct {
		key, prefix string
		seen        map[expr.UserType]bool
		byKind      map[expr.Kind]expr.UserType
		added       []expr.UserType
	}
)

// integerTransportFields replaces unnamed integer values with local named types.
// Named integer definitions keep their primitive body so their own JSON method
// checks the value. Service attributes and their generated Go types are untouched.
func integerTransportFields(attribute *expr.AttributeExpr, key, prefix string) []expr.UserType {
	walker := &integerTransportWalker{key: key, prefix: prefix, seen: make(map[expr.UserType]bool), byKind: make(map[expr.Kind]expr.UserType)}
	walker.visit(attribute)
	return walker.added
}

// visit copies unnamed integer occurrences into one private type per primitive.
// Repeated named definitions are visited once; their own primitive bodies stay
// intact so each named integer can receive its own JSON decoding method.
func (w *integerTransportWalker) visit(current *expr.AttributeExpr) {
	switch actual := current.Type.(type) {
	case expr.UserType:
		if actual == expr.Empty || w.seen[actual] {
			return
		}
		w.seen[actual] = true
		if !isIntegerType(actual) {
			w.visit(actual.Attribute())
		}
	case expr.Primitive:
		// An explicitly substituted Go type owns its own JSON decoding.
		if custom, _ := codegen.GetMetaType(current); custom != "" {
			return
		}
		if !isIntegerType(actual) {
			return
		}
		local := w.byKind[actual.Kind()]
		if local == nil {
			local = &expr.UserTypeExpr{AttributeExpr: &expr.AttributeExpr{Type: actual},
				TypeName: w.prefix + codegen.Goify(actual.Name(), true) + "Transport",
				UID:      "goa-ai-json:" + w.key + ":integer:" + actual.Name()}
			w.byKind[actual.Kind()] = local
			w.added = append(w.added, local)
		}
		current.Type = local
	case *expr.Object:
		for _, field := range *actual {
			w.visit(field.Attribute)
		}
	case *expr.Array:
		w.visit(actual.ElemType)
	case *expr.Map:
		w.visit(actual.KeyType)
		w.visit(actual.ElemType)
	case *expr.Union:
		for _, branch := range actual.Values {
			w.visit(branch.Attribute)
		}
	}
}

// isIntegerType follows named definitions and identifies Go integer values.
// Their private transport types receive the shared exact JSON decoder.
func isIntegerType(dataType expr.DataType) bool {
	for {
		if named, ok := dataType.(expr.UserType); ok && named != expr.Empty {
			dataType = named.Attribute().Type
			continue
		}
		primitive, ok := dataType.(expr.Primitive)
		if !ok {
			return false
		}
		signed, unsigned, _ := jsonshape.IntegerShape(primitive.Kind())
		return signed || unsigned
	}
}

// planIntegerJSON gives each private integer type the shared exact JSON decoder.
// Its generated Go type fixes sign and width; custom types keep their own decoder.
func (v *Value) planIntegerJSON() error {
	if !v.direction.decodes() {
		return nil
	}
	for _, planned := range v.types {
		if custom, _ := codegen.GetMetaType(planned.userType.Attribute()); custom != "" {
			continue
		}
		if !isIntegerType(planned.userType) {
			continue
		}
		if err := v.plan.requireImport(codegen.NewImport("rawjson", "goa.design/goa-ai/runtime/agent/rawjson")); err != nil {
			return err
		}
		planned.integerDecode = true
	}
	return nil
}
