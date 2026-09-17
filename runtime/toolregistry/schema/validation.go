// Package schema validates dynamic tool-registry payloads against
// registry-provided JSON Schemas.
//
// The registry catalog is discovered at runtime, so generated code cannot
// specialize these checks per tool. This package owns JSON Schema compilation
// and caching. Registry consumers use Validate and Codec without depending on
// the concrete validation library.
package schema

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	validator struct {
		mu       sync.RWMutex
		compiled map[string]*jsonschema.Schema
	}

	// schemaLoader supplies the registered schema and prevents a declaration
	// from reading files or fetching another document during compilation.
	schemaLoader struct {
		resource string
		data     []byte
	}
)

var defaultValidator = newValidator()

// Validate validates data against schema. Context names the validated value in
// parse/marshal errors, for example "payload" or "result".
func Validate(schemaBytes []byte, data any, context string) error {
	return defaultValidator.Validate(schemaBytes, data, context)
}

func newValidator() *validator {
	return &validator{
		compiled: make(map[string]*jsonschema.Schema),
	}
}

// Validate validates data against schemaBytes with the package-owned compiler
// cache. JSON numbers retain their exact decimal representation while values
// are checked against the registry's declared constraints.
func (v *validator) Validate(schemaBytes []byte, data any, context string) error {
	schema, err := v.compiledSchema(schemaBytes)
	if err != nil {
		return fmt.Errorf("compile %s schema: %w", context, err)
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s for validation: %w", context, err)
	}

	if _, err := validateJSON(schema, jsonData); err != nil {
		return fmt.Errorf("validate %s: %w", context, err)
	}
	return nil
}

// compiledSchema returns the compiled form of schemaBytes, creating and caching
// it on first use. Cache keys are content digests so repeated generated calls
// share one compiled contract regardless of which registry package supplied it.
func (v *validator) compiledSchema(schemaBytes []byte) (*jsonschema.Schema, error) {
	if len(schemaBytes) == 0 {
		return nil, fmt.Errorf("schema is required")
	}

	digest := fmt.Sprintf("%x", sha256.Sum256(schemaBytes))

	v.mu.RLock()
	schema := v.compiled[digest]
	v.mu.RUnlock()
	if schema != nil {
		return schema, nil
	}

	compiler := jsonschema.NewCompiler()
	resource := "schema://toolregistry/" + digest + ".json"
	compiler.UseLoader(schemaLoader{resource: resource, data: schemaBytes})
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}

	v.mu.Lock()
	if cached := v.compiled[digest]; cached != nil {
		v.mu.Unlock()
		return cached, nil
	}
	v.compiled[digest] = compiled
	v.mu.Unlock()
	return compiled, nil
}

// validateJSON checks exactly one JSON value without rounding its numbers.
func validateJSON(schema *jsonschema.Schema, data []byte) (any, error) {
	var document any
	if err := rawjson.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	if err := schema.Validate(document); err != nil {
		return nil, err
	}
	return document, nil
}

// Load supplies only the schema bytes being compiled. References inside that
// document remain valid; references to a different document are rejected.
func (l schemaLoader) Load(resource string) (any, error) {
	if resource != l.resource {
		return nil, fmt.Errorf("external schema reference %q is not allowed", resource)
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(l.data))
}
