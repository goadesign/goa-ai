package bedrock

// This file owns Bedrock's synthetic-tool representation for structured
// completions. Bedrock tool inputs are objects, while Goa completions may
// return any JSON value, so the adapter wraps values privately and removes the
// wrapper before exposing provider-neutral completion data.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

const structuredOutputValueField = "value"

// structuredOutputToolInput converts an arbitrary completion contract into
// the object-shaped input contract required by Bedrock tools.
func structuredOutputToolInput(output *model.StructuredOutput) (model.ToolInput, error) {
	schema, err := wrapStructuredOutputSchema(output.Schema)
	if err != nil {
		return model.ToolInput{}, err
	}
	var schemaWithoutRootExample rawjson.Message
	if len(output.SchemaWithoutRootExample) > 0 {
		schemaWithoutRootExample, err = wrapStructuredOutputSchema(output.SchemaWithoutRootExample)
		if err != nil {
			return model.ToolInput{}, err
		}
	}
	var example rawjson.Message
	if len(output.ExampleJSON) > 0 {
		example, err = wrapStructuredOutputValue(output.ExampleJSON)
		if err != nil {
			return model.ToolInput{}, err
		}
	}
	return model.ToolInputFromContract(output.Name, model.ToolInputContract{
		Schema:                   schema,
		SchemaWithoutRootExample: schemaWithoutRootExample,
		ExampleJSON:              example,
	})
}

// unwrapStructuredOutputValue validates and removes the private object wrapper
// from one synthetic tool result.
func unwrapStructuredOutputValue(payload rawjson.Message) (rawjson.Message, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	start, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode structured output tool envelope: %w", err)
	}
	if start != json.Delim('{') {
		return nil, errors.New("decode structured output tool envelope: expected object")
	}

	var value json.RawMessage
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode structured output tool envelope field: %w", err)
		}
		field := token.(string)
		if field != structuredOutputValueField {
			return nil, fmt.Errorf(
				"decode structured output tool envelope: unknown field %q",
				field,
			)
		}
		if value != nil {
			return nil, fmt.Errorf(
				"decode structured output tool envelope: duplicate field %q",
				field,
			)
		}
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode structured output tool envelope value: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("decode structured output tool envelope: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode structured output tool envelope: trailing JSON value")
		}
		return nil, fmt.Errorf("decode structured output tool envelope: %w", err)
	}
	if value == nil {
		return nil, errors.New(`structured output tool envelope is missing required field "value"`)
	}
	return append(rawjson.Message(nil), value...), nil
}

func wrapStructuredOutputSchema(schema []byte) (rawjson.Message, error) {
	decoder := json.NewDecoder(bytes.NewReader(schema))
	decoder.UseNumber()
	var inner any
	if err := decoder.Decode(&inner); err != nil {
		return nil, fmt.Errorf("decode structured output tool schema: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode structured output tool schema: trailing JSON value")
	}
	envelope := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{structuredOutputValueField},
		"properties": map[string]any{
			structuredOutputValueField: inner,
		},
	}
	if object, ok := inner.(map[string]any); ok {
		// Local references such as "#/$defs/Draft" start at the wrapped tool
		// schema, not at the schema stored under "value". Move the generated
		// definitions to that root so the runtime and Bedrock resolve the same
		// completion fields the caller declared.
		if definitions, present := object["$defs"]; present {
			envelope["$defs"] = definitions
			delete(object, "$defs")
		}
		if example, present := object["example"]; present {
			envelope["example"] = map[string]any{structuredOutputValueField: example}
			delete(object, "example")
		}
		if examples, present := object["examples"].([]any); present {
			wrapped := make([]any, len(examples))
			for index, example := range examples {
				wrapped[index] = map[string]any{structuredOutputValueField: example}
			}
			envelope["examples"] = wrapped
			delete(object, "examples")
		}
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode structured output tool schema: %w", err)
	}
	return rawjson.Message(data), nil
}

func wrapStructuredOutputValue(value []byte) (rawjson.Message, error) {
	envelope := struct {
		Value json.RawMessage `json:"value"`
	}{
		Value: append(json.RawMessage(nil), value...),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode structured output tool value: %w", err)
	}
	return rawjson.Message(data), nil
}
