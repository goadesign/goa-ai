// Package schema constructs codecs for tool types discovered after compilation.
// Each codec checks both incoming and outgoing JSON against one compiled schema.
// Decoded values use maps, arrays, and JSON primitives with exact numbers.
package schema

import (
	"encoding/json"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// jsonCodec retains one immutable schema for both directions of a tool value.
	jsonCodec struct {
		schema *jsonschema.Schema
	}
)

// Codec compiles a self-contained JSON Schema and returns a validating codec.
// FromJSON returns ordinary JSON values with json.Number for numbers. Results
// therefore follow the runtime's decoded-value contract without rounding or
// requiring a generated Go type for a tool discovered after compilation.
func Codec(schemaBytes []byte) (tools.JSONCodec[any], error) {
	compiled, err := defaultValidator.compiledSchema(schemaBytes)
	if err != nil {
		return tools.JSONCodec[any]{}, err
	}
	codec := jsonCodec{schema: compiled}
	return tools.JSONCodec[any]{ToJSON: codec.encode, FromJSON: codec.decode}, nil
}

// encode checks an application value before returning its JSON representation.
func (c jsonCodec) encode(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if _, err := validateJSON(c.schema, data); err != nil {
		return nil, err
	}
	return data, nil
}

// decode checks provider or model JSON and returns the validated JSON value.
func (c jsonCodec) decode(data []byte) (any, error) {
	return validateJSON(c.schema, data)
}
