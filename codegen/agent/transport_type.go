// Package codegen renders the private JSON types used by generated tool codecs.
//
// Tool payloads and results use ordinary Goa service representations after
// decoding. Their JSON transport counterparts keep pointer presence until
// generated validation has distinguished an omitted field from its zero value.
package codegen

import (
	"fmt"
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// modelJSONTransportContext returns the Goa rendering rules shared by generated
// model JSON types, validators, and codecs. Pointers preserve missing fields and
// null array elements until validation runs. The packageName argument qualifies
// references when codecs live outside the transport package.
func modelJSONTransportContext(
	scope *goacodegen.NameScope,
	pointer bool,
	packageName string,
) *goacodegen.AttributeContext {
	ctx := goacodegen.NewAttributeContext(pointer, false, false, packageName, scope)
	ctx.UnionPointer = true
	ctx.ArrayElementPointer = true
	return ctx
}

// transportTypeDef renders a tool-local JSON type with the field representation
// described by ctx. The attribute graph has already been localized into the
// generated http package, so every named type is resolved through the package's
// single Goa name scope.
func transportTypeDef(
	scope *goacodegen.NameScope,
	att *goaexpr.AttributeExpr,
	ctx *goacodegen.AttributeContext,
) string {
	switch actual := att.Type.(type) {
	case goaexpr.Primitive:
		return scope.GoTypeName(att)
	case *goaexpr.Array:
		definition := transportTypeDef(scope, actual.ElemType, ctx)
		if goaexpr.IsObject(actual.ElemType.Type) || ctx.IsArrayElementPointer(actual) {
			definition = "*" + definition
		}
		return "[]" + definition
	case *goaexpr.Map:
		key := transportTypeDef(scope, actual.KeyType, ctx)
		if goaexpr.IsObject(actual.KeyType.Type) {
			key = "*" + key
		}
		value := transportTypeDef(scope, actual.ElemType, ctx)
		if goaexpr.IsObject(actual.ElemType.Type) {
			value = "*" + value
		}
		return fmt.Sprintf("map[%s]%s", key, value)
	case *goaexpr.Object:
		fields := make([]string, 0, len(*actual)+2)
		fields = append(fields, "struct {")
		for _, field := range *actual {
			name := goacodegen.GoifyAtt(field.Attribute, field.Name, true)
			definition := transportTypeDef(scope, field.Attribute, ctx)
			if ctx.IsFieldPointer(field.Name, att) {
				definition = "*" + definition
			}
			comment := ""
			if field.Attribute.Description != "" {
				comment = goacodegen.Comment(field.Attribute.Description) + "\n\t"
			}
			tags := goacodegen.AttributeTagsWithName(att, field.Name, field.Attribute)
			fields = append(fields, fmt.Sprintf("\t%s%s %s%s", comment, name, definition, tags))
		}
		fields = append(fields, "}")
		return strings.Join(fields, "\n")
	case goaexpr.UserType:
		if actual == goaexpr.Empty {
			return "struct {}"
		}
		return scope.GoTypeName(att)
	case *goaexpr.Union:
		return scope.GoTypeName(att)
	default:
		panic(fmt.Sprintf("agent/codegen: unsupported transport data type %T", actual))
	}
}
