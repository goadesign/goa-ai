// Package contract uses generated field declarations to encode model arguments
// from complete execution values. It removes only fixed fields omitted from the
// text-only model contract and validates every remaining value with that contract.
package contract

import (
	"maps"

	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// textOnlyModelEncoder keeps the strict model codec and the declared fields
	// that execution may carry but a text-only model must not receive.
	textOnlyModelEncoder struct {
		codec   tools.JSONCodec[any]
		removed []string
	}
)

// compileTextOnlyModelCodec compares generated field records once when a tool
// is admitted. Decoding stays strict; encoding a complete execution object
// omits only the fields removed from the ordinary model declaration.
func compileTextOnlyModelCodec(ordinary, selected tools.TypeSpec) tools.JSONCodec[any] {
	visible := make(map[tools.FixedField]bool)
	for _, field := range selected.Fields {
		if len(field.Path) == 1 {
			if name, ok := field.Path[0].(tools.FixedField); ok {
				visible[name] = true
			}
		}
	}
	var removed []string
	for _, field := range ordinary.Fields {
		if len(field.Path) == 1 {
			if name, ok := field.Path[0].(tools.FixedField); ok && !visible[name] {
				removed = append(removed, string(name))
			}
		}
	}
	if len(removed) == 0 {
		return selected.Codec
	}
	encoder := textOnlyModelEncoder{codec: selected.Codec, removed: removed}
	return tools.JSONCodec[any]{ToJSON: encoder.toJSON, FromJSON: selected.Codec.FromJSON}
}

// toJSON copies an execution object before omitting declared controls, so the
// executor's arguments remain unchanged. Scalars and collections keep their
// original shape and are accepted or rejected by the selected model codec.
func (encoder textOnlyModelEncoder) toJSON(value any) ([]byte, error) {
	if fields, ok := value.(map[string]any); ok {
		copy := maps.Clone(fields)
		for _, name := range encoder.removed {
			delete(copy, name)
		}
		value = copy
	}
	return encoder.codec.ToJSON(value)
}
