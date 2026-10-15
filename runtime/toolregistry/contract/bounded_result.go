// Package contract compiles provider result codecs from registry declarations.
// Providers return result JSON with separate Bounds containing counts,
// truncation, and paging information. The private codec validates result data
// before the runtime adds these fields for callers; public schemas stay unchanged.
package contract

import (
	"encoding/json"
	"fmt"
	"slices"

	"goa.design/goa-ai/boundedresult"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

// compileBoundedResultCodec takes a validated public result schema and the
// tool's declared bounds. It makes only fields supplied by the runtime optional
// in the private codec, so provider data can be validated before those fields
// are added. Present fields keep their types and constraints, and callers still
// receive results described by the unchanged public schema.
func compileBoundedResultCodec(document []byte, bounds *genregistry.ToolBounds) (tools.JSONCodec[any], error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(document, &object); err != nil {
		return tools.JSONCodec[any]{}, fmt.Errorf("read bounded result schema: %w", err)
	}
	var required []string
	if value, exists := object["required"]; exists {
		if err := json.Unmarshal(value, &required); err != nil {
			return tools.JSONCodec[any]{}, fmt.Errorf("read bounded result requirements: %w", err)
		}
	}
	nextCursorField := ""
	if bounds.Paging != nil {
		nextCursorField = bounds.Paging.NextCursorField
	}
	owned := boundedresult.CanonicalFieldNames(nextCursorField)
	required = slices.DeleteFunc(required, func(name string) bool {
		return slices.Contains(owned, name)
	})
	if len(required) == 0 {
		delete(object, "required")
	} else {
		encoded, err := json.Marshal(required)
		if err != nil {
			return tools.JSONCodec[any]{}, fmt.Errorf("encode bounded result requirements: %w", err)
		}
		object["required"] = encoded
	}
	private, err := json.Marshal(object)
	if err != nil {
		return tools.JSONCodec[any]{}, fmt.Errorf("encode bounded result schema: %w", err)
	}
	return schema.Codec(private)
}
