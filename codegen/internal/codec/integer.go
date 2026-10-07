// Package codec gives private integer fields the JSON number contract declared
// by their schema. The generated decoder accepts equivalent decimal and exponent
// spellings, then checks the authored Go integer range without using floats.
package codec

import (
	"fmt"

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
		if _, ok := integerPrimitive(actual); !ok {
			w.visit(actual.Attribute())
		}
	case expr.Primitive:
		// An explicitly substituted Go type owns its own JSON decoding.
		if custom, _ := codegen.GetMetaType(current); custom != "" {
			return
		}
		if _, ok := integerPrimitive(actual); !ok {
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

// integerPrimitive identifies the primitive behind a named integer definition.
// The caller uses its signedness and width to emit only its required decoder.
func integerPrimitive(dataType expr.DataType) (expr.Primitive, bool) {
	for {
		if named, ok := dataType.(expr.UserType); ok && named != expr.Empty {
			dataType = named.Attribute().Type
			continue
		}
		primitive, ok := dataType.(expr.Primitive)
		if !ok {
			return 0, false
		}
		signed, unsigned, _ := jsonshape.IntegerShape(primitive.Kind())
		return primitive, signed || unsigned
	}
}

// planIntegerJSON reserves one exact-number helper when a value needs decoding.
// Each integer transport type calls it before checking its own Go integer range.
func (v *Value) planIntegerJSON() error {
	if !v.direction.decodes() {
		return nil
	}
	for _, planned := range v.types {
		if custom, _ := codegen.GetMetaType(planned.userType.Attribute()); custom != "" {
			continue
		}
		if _, ok := integerPrimitive(planned.userType); !ok {
			continue
		}
		if v.plan.integerJSON == nil {
			name := codegen.NewPreferredName(codegen.NameFunction, "integerJSONText", codegen.UnexportedName,
				nameOrder{packagePath: v.plan.pkg.ImportPath(), key: "shared-json:integer"})
			if err := v.plan.pkg.DeclareName(name); err != nil {
				return err
			}
			v.plan.integerJSON = name
			for _, name := range []string{"strconv", "strings"} {
				if err := v.plan.requireImport(codegen.NewImport(name, name)); err != nil {
					return fmt.Errorf("integer JSON import: %w", err)
				}
			}
		}
		planned.integerDecode = true
	}
	return nil
}

// integerSource converts one valid JSON number to exact integer text. It never
// expands an exponent beyond the widest native integer representation; each
// generated field decoder then applies its own signedness and width.
const integerSource = `
// {{ .IntegerJSON }} reads a JSON number and returns the same whole number in
// decimal notation. Fractional values and values beyond native integer ranges fail.
func {{ .IntegerJSON }}(data []byte) (string, error) {
 text := string({{ .Imports.Bytes }}.TrimSpace(data))
 if len(text) == 0 || (text[0] != '-' && (text[0] < '0' || text[0] > '9')) || !{{ .Imports.JSON }}.Valid([]byte(text)) {
  return "", {{ .Imports.Fmt }}.Errorf("expected an integer JSON number")
 }
 negative := text[0] == '-'
 if negative { text = text[1:] }
 coefficient, exponentText, hasExponent := {{ .Imports.Strings }}.Cut(text, "e")
 if !hasExponent { coefficient, exponentText, hasExponent = {{ .Imports.Strings }}.Cut(text, "E") }
 whole, fraction, _ := {{ .Imports.Strings }}.Cut(coefficient, ".")
 digits := {{ .Imports.Strings }}.TrimLeft(whole + fraction, "0")
 if digits == "" { return "0", nil }
 exponent := 0
 if hasExponent {
  parsed, err := {{ .Imports.Strconv }}.Atoi(exponentText)
  if err != nil { return "", {{ .Imports.Fmt }}.Errorf("JSON number cannot be represented as an integer") }
  exponent = parsed
 }
 // A negative exponent cannot cancel more digits than the input contains.
 // Comparing before subtraction also prevents overflow for extreme exponents.
 if exponent < -len(digits) || exponent > len(fraction) + len({{ .Imports.Strconv }}.FormatUint(^uint64(0), 10)) {
  return "", {{ .Imports.Fmt }}.Errorf("JSON number cannot be represented as an integer")
 }
 scale := exponent - len(fraction)
 if scale < 0 {
  removed := -scale
  if removed >= len(digits) || {{ .Imports.Strings }}.Trim(digits[len(digits)-removed:], "0") != "" {
   return "", {{ .Imports.Fmt }}.Errorf("expected a whole JSON number")
  }
  digits = digits[:len(digits)-removed]
  scale = 0
 }
 if len(digits) + scale > len({{ .Imports.Strconv }}.FormatUint(^uint64(0), 10)) {
  return "", {{ .Imports.Fmt }}.Errorf("JSON number cannot be represented as an integer")
 }
 digits += {{ .Imports.Strings }}.Repeat("0", scale)
 if negative { digits = "-" + digits }
 return digits, nil
}
`
