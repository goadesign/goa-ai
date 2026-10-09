// Package mcp validates accepted MCP form values as flat primitives and string
// selections. The shared schema compiler checks the requested constraints.
package mcp

import (
	"sync"

	schema "github.com/santhosh-tekuri/jsonschema/v6"

	"goa.design/goa-ai/internal/jsonschema"
)

// Accepted form values remain flat even when the requested schema permits extra keys.
var compileFormContent = sync.OnceValues(func() (*schema.Schema, error) {
	return jsonschema.Compile([]byte(`{"type":"object","additionalProperties":{"anyOf":[{"type":"string"},{"type":"number"},{"type":"boolean"},{"type":"array","items":{"type":"string"}}]}}`))
})
