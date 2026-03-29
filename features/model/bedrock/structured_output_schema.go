package bedrock

import (
	"encoding/json"
	"fmt"
)

// structured_output_schema.go adapts canonical structured-output schemas to the
// constrained JSON Schema subset accepted by Bedrock. The generated schema
// stays provider-neutral and remains the source of truth for local decoding;
// this adapter either produces an equivalent Bedrock schema or rejects the
// contract explicitly when Bedrock cannot represent it.

const (
	bedrockSchemaTypeArray  = "array"
	bedrockSchemaTypeObject = "object"
	bedrockSchemaTypeString = "string"
)

var (
	bedrockSupportedStringFormats = map[string]struct{}{
		"date-time": {},
		"time":      {},
		"date":      {},
		"duration":  {},
		"email":     {},
		"hostname":  {},
		"uri":       {},
		"ipv4":      {},
		"ipv6":      {},
		"uuid":      {},
	}

	bedrockUnsupportedSchemaKeywords = map[string]struct{}{
		"$schema":               {},
		"title":                 {},
		"default":               {},
		"example":               {},
		"examples":              {},
		"minimum":               {},
		"maximum":               {},
		"exclusiveMinimum":      {},
		"exclusiveMaximum":      {},
		"multipleOf":            {},
		"minLength":             {},
		"maxLength":             {},
		"pattern":               {},
		"maxItems":              {},
		"minProperties":         {},
		"maxProperties":         {},
		"uniqueItems":           {},
		"unevaluatedProperties": {},
		"unevaluatedItems":      {},
		"contentEncoding":       {},
		"contentMediaType":      {},
		"contentSchema":         {},
		"readOnly":              {},
		"writeOnly":             {},
		"deprecated":            {},
	}
)

// normalizeStructuredOutputSchemaForBedrock rewrites one structured-output
// schema into the subset Bedrock supports for constrained decoding.
func normalizeStructuredOutputSchemaForBedrock(schema []byte) ([]byte, error) {
	if len(schema) == 0 {
		return nil, nil
	}

	var doc any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, fmt.Errorf("bedrock: invalid structured output schema JSON: %w", err)
	}
	if err := normalizeBedrockSchemaNode(doc, "$"); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("bedrock: marshal structured output schema: %w", err)
	}
	return normalized, nil
}

// normalizeBedrockSchemaNode rewrites one JSON Schema node in place and
// recurses through its children. It strips keywords Bedrock does not support,
// closes implicit object schemas, and rejects map-style additionalProperties
// contracts that Bedrock cannot encode.
func normalizeBedrockSchemaNode(node any, path string) error {
	switch value := node.(type) {
	case map[string]any:
		for key := range bedrockUnsupportedSchemaKeywords {
			delete(value, key)
		}
		normalizeBedrockSchemaFormat(value)
		if includesSchemaType(value, bedrockSchemaTypeObject) {
			if err := normalizeBedrockObjectSchema(value, path); err != nil {
				return err
			}
		}
		if includesSchemaType(value, bedrockSchemaTypeArray) {
			normalizeBedrockArraySchema(value)
		}
		for key, child := range value {
			if err := normalizeBedrockSchemaNode(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range value {
			if err := normalizeBedrockSchemaNode(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// normalizeBedrockObjectSchema enforces Bedrock's closed-object contract.
// Objects must declare additionalProperties:false; map-style schemas are
// rejected because Bedrock structured outputs cannot represent them.
func normalizeBedrockObjectSchema(node map[string]any, path string) error {
	additional, exists := node["additionalProperties"]
	if !exists || additional == nil {
		node["additionalProperties"] = false
		return nil
	}

	enabled, ok := additional.(bool)
	if !ok {
		return fmt.Errorf(
			"bedrock: structured output object schema at %s uses additionalProperties that is not false",
			path,
		)
	}
	if enabled {
		return fmt.Errorf(
			"bedrock: structured output object schema at %s must set additionalProperties to false",
			path,
		)
	}
	return nil
}

// normalizeBedrockArraySchema keeps only the minItems values Bedrock supports.
func normalizeBedrockArraySchema(node map[string]any) {
	minItems, ok := jsonNumberAsInt(node["minItems"])
	if !ok {
		delete(node, "minItems")
		return
	}
	if minItems != 0 && minItems != 1 {
		delete(node, "minItems")
	}
}

// normalizeBedrockSchemaFormat keeps only the string formats Bedrock supports.
func normalizeBedrockSchemaFormat(node map[string]any) {
	raw, ok := node["format"]
	if !ok {
		return
	}
	format, ok := raw.(string)
	if !ok {
		delete(node, "format")
		return
	}
	if !includesSchemaType(node, bedrockSchemaTypeString) {
		delete(node, "format")
		return
	}
	if _, ok := bedrockSupportedStringFormats[format]; !ok {
		delete(node, "format")
	}
}

// includesSchemaType reports whether a JSON Schema node declares the requested
// type, including union forms such as ["string","null"].
func includesSchemaType(node map[string]any, want string) bool {
	switch value := node["type"].(type) {
	case string:
		return value == want
	case []any:
		for _, elem := range value {
			if typ, ok := elem.(string); ok && typ == want {
				return true
			}
		}
	}
	return false
}

// jsonNumberAsInt decodes a JSON number previously unmarshaled into an untyped
// schema tree.
func jsonNumberAsInt(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	return int64(number), number == float64(int64(number))
}
