// Package jsonshape recognizes one map whose JSON members appear beside ordinary fields.
// Generators reject declarations whose required presence or size cannot be
// represented by the same open object in both JSON and its schema.
package jsonshape

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// EmbeddedMap checks an object's JSON tags and returns its extra-member map.
// Named JSON fields remain ordinary fields even when their tag contains embed.
// Unsupported declarations return an error before generators emit a codec.
func EmbeddedMap(attribute *expr.AttributeExpr) (*expr.NamedAttributeExpr, error) {
	object := expr.AsObject(attribute.Type)
	if object == nil {
		return nil, nil
	}
	var found *expr.NamedAttributeExpr
	for _, field := range *object {
		tags := strings.Trim(codegen.AttributeTagsWithName(nil, "", field.Attribute), " `")
		tag := reflect.StructTag(tags).Get("json")
		parts := strings.Split(tag, ",")
		if parts[0] != "" || !slices.Contains(parts[1:], "embed") {
			continue
		}
		if len(parts) != 2 {
			return nil, fmt.Errorf("embedded JSON map %q must use only the embed option", field.Name)
		}
		if found != nil {
			return nil, fmt.Errorf("JSON object has multiple embedded maps %q and %q", found.Name, field.Name)
		}
		values := expr.AsMap(field.Attribute.Type)
		if values == nil {
			return nil, fmt.Errorf("embedded JSON field %q must be a string-key map", field.Name)
		}
		key, ok := PrimitiveType(values.KeyType)
		if !ok || key != expr.String {
			return nil, fmt.Errorf("embedded JSON map %q must have string keys", field.Name)
		}
		if attribute.IsRequired(field.Name) {
			return nil, fmt.Errorf("embedded JSON map %q cannot be required", field.Name)
		}
		validation := expr.EffectiveValidation(field.Attribute)
		if validation != nil && (validation.MinLength != nil || validation.MaxLength != nil) {
			return nil, fmt.Errorf("embedded JSON map %q cannot have length bounds", field.Name)
		}
		found = field
	}
	if found != nil {
		for _, field := range *object {
			if field == found {
				continue
			}
			tags := strings.Trim(codegen.AttributeTagsWithName(nil, "", field.Attribute), " `")
			tag := reflect.StructTag(tags).Get("json")
			if tag == "-" {
				continue
			}
			parts := strings.Split(tag, ",")
			if parts[0] == "" {
				return nil, fmt.Errorf("JSON field %q beside an embedded map requires an explicit JSON name", field.Name)
			}
			if parts[0] == "-" || strings.ContainsAny(parts[0], "'\"`\\") {
				return nil, fmt.Errorf("JSON field %q beside an embedded map requires a literal JSON name", field.Name)
			}
			options := parts[1:]
			if !slices.Contains(options, "case:strict") || slices.Contains(options, "case:ignore") {
				return nil, fmt.Errorf("JSON field %q beside an embedded map requires case:strict to preserve case-distinct extra members", field.Name)
			}
		}
	}
	return found, nil
}
