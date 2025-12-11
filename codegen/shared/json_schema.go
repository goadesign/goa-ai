// Package shared provides common utilities for code generation across protocols.
package shared

import (
	"encoding/json"

	"goa.design/goa/v3/expr"
)

// JSON Schema type constants.
const (
	jsonTypeObject  = "object"
	jsonTypeArray   = "array"
	jsonTypeString  = "string"
	jsonTypeInteger = "integer"
	jsonTypeNumber  = "number"
	jsonTypeBoolean = "boolean"
)

// ToJSONSchema returns a compact JSON Schema for the given Goa attribute.
// It generates inline schemas without $ref references, which is required
// for MCP protocols that expect fully resolved schemas.
//
// This function is shared between MCP code generation.
func ToJSONSchema(attr *expr.AttributeExpr) string {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return `{"type":"object","additionalProperties":false}`
	}

	schema := buildInlineSchema(attr)
	b, err := json.Marshal(schema)
	if err != nil {
		return `{"type":"object","additionalProperties":false}`
	}
	return string(b)
}

// inlineSchema represents a JSON Schema without $ref references.
// Field names use camelCase per JSON Schema specification.
//
//nolint:tagliatelle // JSON Schema specification requires camelCase field names
type inlineSchema struct {
	Type                 string                   `json:"type,omitempty"`
	Description          string                   `json:"description,omitempty"`
	Required             []string                 `json:"required,omitempty"`
	Properties           map[string]*inlineSchema `json:"properties,omitempty"`
	Items                *inlineSchema            `json:"items,omitempty"`
	AdditionalProperties any                      `json:"additionalProperties,omitempty"`
	Enum                 []any                    `json:"enum,omitempty"`
	Default              any                      `json:"default,omitempty"`
	Minimum              *float64                 `json:"minimum,omitempty"`
	Maximum              *float64                 `json:"maximum,omitempty"`
	MinLength            *int                     `json:"minLength,omitempty"`
	MaxLength            *int                     `json:"maxLength,omitempty"`
	Pattern              string                   `json:"pattern,omitempty"`
	Format               string                   `json:"format,omitempty"`
	MinItems             *int                     `json:"minItems,omitempty"`
	MaxItems             *int                     `json:"maxItems,omitempty"`
}

// buildInlineSchema creates a JSON Schema from a Goa attribute without $ref.
func buildInlineSchema(attr *expr.AttributeExpr) *inlineSchema {
	if attr == nil || attr.Type == nil {
		return &inlineSchema{Type: jsonTypeObject, AdditionalProperties: false}
	}

	schema := &inlineSchema{
		Description: attr.Description,
	}

	// Handle default value
	if attr.DefaultValue != nil {
		schema.Default = attr.DefaultValue
	}

	// Handle validations
	if v := attr.Validation; v != nil {
		if len(v.Values) > 0 {
			schema.Enum = v.Values
		}
		if v.Minimum != nil {
			schema.Minimum = v.Minimum
		}
		if v.Maximum != nil {
			schema.Maximum = v.Maximum
		}
		if v.MinLength != nil {
			schema.MinLength = v.MinLength
		}
		if v.MaxLength != nil {
			schema.MaxLength = v.MaxLength
		}
		if v.Pattern != "" {
			schema.Pattern = v.Pattern
		}
		if v.Format != "" {
			schema.Format = string(v.Format)
		}
	}

	switch t := attr.Type.(type) {
	case expr.Primitive:
		schema.Type = primitiveToJSONType(t)
	case *expr.Array:
		schema.Type = jsonTypeArray
		if t.ElemType != nil {
			schema.Items = buildInlineSchema(t.ElemType)
		}
		if v := attr.Validation; v != nil {
			if v.MinLength != nil {
				schema.MinItems = v.MinLength
				schema.MinLength = nil
			}
			if v.MaxLength != nil {
				schema.MaxItems = v.MaxLength
				schema.MaxLength = nil
			}
		}
	case *expr.Map:
		schema.Type = jsonTypeObject
		if t.ElemType != nil {
			schema.AdditionalProperties = buildInlineSchema(t.ElemType)
		} else {
			schema.AdditionalProperties = true
		}
	case *expr.Object:
		schema.Type = jsonTypeObject
		schema.Properties = make(map[string]*inlineSchema)
		for _, nat := range *t {
			schema.Properties[nat.Name] = buildInlineSchema(nat.Attribute)
		}
		schema.AdditionalProperties = false
		// Collect required fields
		if attr.Validation != nil && len(attr.Validation.Required) > 0 {
			schema.Required = attr.Validation.Required
		}
	case *expr.UserTypeExpr:
		// Inline the user type's attribute
		return buildInlineSchema(t.Attribute())
	case *expr.ResultTypeExpr:
		// Inline the result type's attribute
		return buildInlineSchema(t.Attribute())
	default:
		// Fallback for unknown types
		schema.Type = jsonTypeObject
		schema.AdditionalProperties = false
	}

	return schema
}

// primitiveToJSONType converts a Goa primitive type to JSON Schema type.
func primitiveToJSONType(p expr.Primitive) string {
	switch p {
	case expr.Boolean:
		return jsonTypeBoolean
	case expr.Int, expr.Int32, expr.Int64, expr.UInt, expr.UInt32, expr.UInt64:
		return jsonTypeInteger
	case expr.Float32, expr.Float64:
		return jsonTypeNumber
	case expr.String:
		return jsonTypeString
	case expr.Bytes:
		return jsonTypeString
	case expr.Any:
		return jsonTypeObject
	default:
		return jsonTypeString
	}
}
