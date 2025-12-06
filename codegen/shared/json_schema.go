// Package shared provides common utilities for code generation across protocols.
package shared

import (
	"encoding/json"
	"sync"

	"goa.design/goa/v3/expr"
	"goa.design/goa/v3/http/codegen/openapi"
)

// schemaLock protects access to the global openapi.Definitions map
// since it's not thread-safe.
var schemaLock sync.Mutex

// minimalAPI is a minimal API expression used for schema generation.
// It provides an ExampleGenerator which is required by Goa's openapi package.
var minimalAPI = &expr.APIExpr{
	Name:             "schema",
	ExampleGenerator: &expr.ExampleGenerator{Randomizer: expr.NewDeterministicRandomizer()},
}

// ToJSONSchema returns a compact JSON Schema for the given Goa attribute.
// It uses Goa's openapi package for schema generation, which provides
// comprehensive support for all Goa types including primitives, objects,
// arrays, maps, unions, and user types.
//
// This function is shared between MCP and A2A code generation.
func ToJSONSchema(attr *expr.AttributeExpr) string {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return `{"type":"object","additionalProperties":false}`
	}

	// Lock to protect the global Definitions map in openapi package
	schemaLock.Lock()
	defer schemaLock.Unlock()

	// Save and restore definitions to avoid pollution between calls
	oldDefs := openapi.Definitions
	openapi.Definitions = make(map[string]*openapi.Schema)
	defer func() { openapi.Definitions = oldDefs }()

	// Use Goa's openapi package to generate the schema
	schema := openapi.AttributeTypeSchema(minimalAPI, attr)

	// For objects without explicit additionalProperties, set to false
	// for stricter validation (MCP/A2A tools expect exact schemas)
	if schema.Type == openapi.Object && schema.AdditionalProperties == nil {
		schema.AdditionalProperties = false
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return `{"type":"object","additionalProperties":false}`
	}
	return string(b)
}
