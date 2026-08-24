// Package model validates caller-authored tool payloads before model output is
// exposed. Generated tools retain their generated decoder; tools defined from
// raw JSON Schema compile that schema once and reuse it for every returned call.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

const maxToolSchemaBytes = 1 << 20

// rejectingSchemaLoader prevents caller-authored schemas from reading network
// or local files while the compiler resolves references.
type rejectingSchemaLoader struct{}

// Load rejects every external schema reference.
func (rejectingSchemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not allowed", url)
}

// compileToolSchemaValidator compiles one caller-authored schema and returns
// the exact payload check captured by the request contract.
func compileToolSchemaValidator(schemaBytes rawjson.Message) (func(rawjson.Message) error, error) {
	if len(schemaBytes) > maxToolSchemaBytes {
		return nil, fmt.Errorf("tool schema exceeds %d bytes", maxToolSchemaBytes)
	}
	schemaDocument, err := decodeSingleJSONDocument(schemaBytes)
	if err != nil {
		return nil, fmt.Errorf("decode tool schema: %w", err)
	}
	schemaObject, ok := schemaDocument.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool schema root must be an object schema")
	}
	if declared, ok := schemaObject["type"].(string); !ok || declared != jsonObjectType {
		return nil, fmt.Errorf(`tool schema root must declare type "object"`)
	}
	if err := validateCanonicalDynamicValue(reflect.ValueOf(schemaDocument)); err != nil {
		return nil, fmt.Errorf("bound tool schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(rejectingSchemaLoader{})
	const resource = "schema://goa-ai/model/tool-input.json"
	if err := compiler.AddResource(resource, schemaDocument); err != nil {
		return nil, fmt.Errorf("add tool schema: %w", err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("compile tool schema: %w", err)
	}
	return func(payload rawjson.Message) error {
		document, err := decodeSingleJSONDocument(payload)
		if err != nil {
			return fmt.Errorf("decode JSON: %w", err)
		}
		if err := schema.Validate(document); err != nil {
			return fmt.Errorf("validate JSON Schema: %w", err)
		}
		return nil
	}, nil
}

// decodeSingleJSONDocument decodes one complete JSON value with exact numbers.
// Trailing non-whitespace bytes are rejected instead of being ignored.
func decodeSingleJSONDocument(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return document, nil
}
