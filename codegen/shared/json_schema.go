// Package shared provides common utilities for code generation across protocols.
package shared

import (
	"encoding/json"
	"fmt"

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
func ToJSONSchema(attr *expr.AttributeExpr) (string, error) {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return `{"type":"object","additionalProperties":false}`, nil
	}

	schema, err := buildInlineSchema(attr, make(map[any]struct{}))
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("marshal JSON schema: %w", err)
	}
	return string(b), nil
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
func buildInlineSchema(attr *expr.AttributeExpr, visited map[any]struct{}) (*inlineSchema, error) {
	if attr == nil || attr.Type == nil {
		return &inlineSchema{Type: jsonTypeObject, AdditionalProperties: false}, nil
	}

	schema := &inlineSchema{}

	switch t := attr.Type.(type) {
	case expr.Primitive:
		schema.Type = primitiveToJSONType(t)
	case *expr.Array:
		schema.Type = jsonTypeArray
		if t.ElemType != nil {
			items, err := buildInlineSchema(t.ElemType, visited)
			if err != nil {
				return nil, err
			}
			schema.Items = items
		}
	case *expr.Map:
		schema.Type = jsonTypeObject
		if t.ElemType != nil {
			properties, err := buildInlineSchema(t.ElemType, visited)
			if err != nil {
				return nil, err
			}
			schema.AdditionalProperties = properties
		} else {
			schema.AdditionalProperties = true
		}
	case *expr.Object:
		schema.Type = jsonTypeObject
		schema.Properties = make(map[string]*inlineSchema)
		for _, nat := range *t {
			property, err := buildInlineSchema(nat.Attribute, visited)
			if err != nil {
				return nil, err
			}
			schema.Properties[nat.Name] = property
		}
		schema.AdditionalProperties = false
	case *expr.UserTypeExpr:
		return inlineWrappedSchema(attr, t.Attribute(), visited, t, t.TypeName)
	case *expr.ResultTypeExpr:
		return inlineWrappedSchema(attr, t.Attribute(), visited, t, t.TypeName)
	default:
		schema.Type = jsonTypeObject
		schema.AdditionalProperties = false
	}

	applyAttributeMetadata(schema, attr)
	return schema, nil
}

// inlineWrappedSchema expands a user or result type while preserving the
// wrapper attribute metadata that referred to it.
func inlineWrappedSchema(
	wrapper *expr.AttributeExpr,
	inner *expr.AttributeExpr,
	visited map[any]struct{},
	identity any,
	typeName string,
) (*inlineSchema, error) {
	if _, ok := visited[identity]; ok {
		return nil, fmt.Errorf("recursive user type %q cannot be converted to inline JSON Schema", typeName)
	}
	visited[identity] = struct{}{}
	defer delete(visited, identity)

	schema, err := buildInlineSchema(inner, visited)
	if err != nil {
		return nil, err
	}
	applyAttributeMetadata(schema, wrapper)
	return schema, nil
}

// applyAttributeMetadata copies attribute-level schema metadata onto an inline
// schema. Wrappers override the inlined type they refer to.
func applyAttributeMetadata(schema *inlineSchema, attr *expr.AttributeExpr) {
	if schema == nil || attr == nil {
		return
	}
	if attr.Description != "" {
		schema.Description = attr.Description
	}
	if attr.DefaultValue != nil {
		schema.Default = attr.DefaultValue
	}
	if attr.Validation == nil {
		return
	}
	v := attr.Validation
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
	if len(v.Required) > 0 {
		schema.Required = v.Required
	}
	if schema.Type == jsonTypeArray {
		if v.MinLength != nil {
			schema.MinItems = v.MinLength
			schema.MinLength = nil
		}
		if v.MaxLength != nil {
			schema.MaxItems = v.MaxLength
			schema.MaxLength = nil
		}
	}
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
