// Package model compiles caller-authored JSON Schema without allowing external
// files or network references. Request contracts retain the resulting private
// validators and apply them before model output is exposed.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

const maxToolSchemaBytes = 1 << 20

type (
	// rawSchemaLoader gives the JSON Schema compiler one in-memory raw document
	// and rejects references that would read another resource.
	rawSchemaLoader struct {
		resource string
		schema   rawjson.Message
	}

	// toolSchemaType retains only the root type used by the framework's
	// object-argument invariant.
	toolSchemaType struct {
		Type json.RawMessage `json:"type"`
	}

	// toolSchemaArguments retains the root fields used to identify tools whose
	// declared object contains no model-authored properties.
	toolSchemaArguments struct {
		Type       json.RawMessage `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
)

// Load lets jsonschema/v6 parse the untouched root bytes from an io.Reader.
// Any other URL is an external reference and is rejected.
func (l rawSchemaLoader) Load(url string) (any, error) {
	if url != l.resource {
		return nil, fmt.Errorf("external schema reference %q is not allowed", url)
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(l.schema))
}

// compileToolSchemaValidator preserves the object-root contract for model tool
// arguments, then compiles the schema used to check each returned tool call.
func compileToolSchemaValidator(schemaBytes rawjson.Message) (func(rawjson.Message) error, error) {
	var root toolSchemaType
	err := decodeToolSchemaRoot(schemaBytes, &root)
	if err != nil {
		return nil, fmt.Errorf("decode tool schema: %w", err)
	}
	var declared string
	if err := json.Unmarshal(root.Type, &declared); err != nil || declared != jsonObjectType {
		return nil, fmt.Errorf(`tool schema root must declare type "object"`)
	}
	return compileJSONSchemaValidator(schemaBytes)
}

// compileJSONSchemaValidator compiles one complete JSON Schema document and
// returns a validator that accepts every JSON root type allowed by that schema.
func compileJSONSchemaValidator(schemaBytes rawjson.Message) (func(rawjson.Message) error, error) {
	const resource = "schema://goa-ai/model/contract.json"
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(rawSchemaLoader{resource: resource, schema: schemaBytes})
	schema, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("compile JSON Schema: %w", err)
	}
	return func(payload rawjson.Message) error {
		document, err := decodeCandidateJSONValue(payload)
		if err != nil {
			return fmt.Errorf("decode candidate JSON: %w", err)
		}
		if err := schema.Validate(document); err != nil {
			return fmt.Errorf("validate JSON Schema: %w", err)
		}
		return nil
	}, nil
}

// decodeToolSchemaRoot parses only the root fields represented by destination.
// Nested schema values remain raw for the JSON Schema compiler.
func decodeToolSchemaRoot(data rawjson.Message, destination any) error {
	if len(data) > maxToolSchemaBytes {
		return fmt.Errorf("tool schema exceeds %d bytes", maxToolSchemaBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// decodeCandidateJSONValue decodes one complete model-authored value with
// json.Number so validation never rounds large integers. Schema bytes use the
// JSON Schema compiler's raw resource loader instead.
func decodeCandidateJSONValue(data rawjson.Message) (any, error) {
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
