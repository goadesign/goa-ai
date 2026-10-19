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

type (
	// textOnlyExecutionDefaults reads only the fixed controls already declared
	// by generated execution schemas, including the two validated page branches.
	textOnlyExecutionDefaults struct {
		Properties map[string]struct {
			Default any   `json:"default"`
			Enum    []any `json:"enum"`
		} `json:"properties"`
		OneOf []textOnlyExecutionDefaults `json:"oneOf"` //nolint:tagliatelle // JSON Schema defines the oneOf member.
	}
)

// compileTextOnlyExecutionCodec reads fixed false defaults when a tool is
// registered. Decoding then supplies these controls before an executor receives
// the call, while the underlying schema still rejects true or invalid inputs.
// A nonempty cursorField means the caller has checked the two paging branches.
func compileTextOnlyExecutionCodec(document []byte, cursorField string) (tools.JSONCodec[any], error) {
	codec, err := schema.Codec(document)
	if err != nil {
		return tools.JSONCodec[any]{}, err
	}
	var declaration textOnlyExecutionDefaults
	if err := json.Unmarshal(document, &declaration); err != nil {
		return tools.JSONCodec[any]{}, fmt.Errorf("read text-only execution defaults: %w", err)
	}
	controls := declaration.controls()
	var firstControls, pageControls []string
	if cursorField != "" {
		firstControls = declaration.OneOf[0].controls()
		pageControls = declaration.OneOf[1].controls()
	}
	if len(controls)+len(firstControls)+len(pageControls) == 0 {
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
			selected := controls
			if cursorField != "" {
				selected = firstControls
				if _, page := fields[cursorField]; page {
					selected = pageControls
				}
			}
			for _, name := range selected {
				if _, supplied := fields[name]; !supplied {
					fields[name] = false
				}
			}
			return fields, nil
		},
	}, nil
}

// controls returns only fields whose generated declaration fixes a missing
// value to false. Other defaults keep their existing decoding behavior.
func (declaration textOnlyExecutionDefaults) controls() []string {
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
	return controls
}
