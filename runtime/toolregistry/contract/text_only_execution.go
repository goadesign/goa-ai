// Package contract compiles generated registry declarations into executable
// tool contracts. Text-only execution adds only the fixed false defaults
// declared by its generated schema; model input remains separately validated.
package contract

import (
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

// compileTextOnlyExecutionCodec reads fixed false defaults when a tool is
// registered. Decoding then supplies these controls before an executor receives
// the call, while the underlying schema still rejects true or invalid inputs.
func compileTextOnlyExecutionCodec(document []byte) (tools.JSONCodec[any], error) {
	codec, err := schema.Codec(document)
	if err != nil {
		return tools.JSONCodec[any]{}, err
	}
	var declaration struct {
		Properties map[string]struct {
			Default any   `json:"default"`
			Enum    []any `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(document, &declaration); err != nil {
		return tools.JSONCodec[any]{}, fmt.Errorf("read text-only execution defaults: %w", err)
	}
	var controls []string
	for name, property := range declaration.Properties {
		disabled, hasDefault := property.Default.(bool)
		if !hasDefault || disabled || len(property.Enum) != 1 {
			continue
		}
		onlyValue, boolean := property.Enum[0].(bool)
		if boolean && !onlyValue {
			controls = append(controls, name)
		}
	}
	if len(controls) == 0 {
		return codec, nil
	}
	return tools.JSONCodec[any]{
		ToJSON: codec.ToJSON,
		FromJSON: func(data []byte) (any, error) {
			value, err := codec.FromJSON(data)
			if err != nil {
				return nil, err
			}
			fields, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("text-only execution controls require an object, received %T", value)
			}
			for _, name := range controls {
				if _, supplied := fields[name]; !supplied {
					fields[name] = false
				}
			}
			return fields, nil
		},
	}, nil
}
