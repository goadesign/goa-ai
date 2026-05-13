// Package schema validates dynamic tool-registry payloads against
// registry-provided JSON Schemas.
//
// The registry catalog is discovered at runtime, so generated code cannot
// specialize these checks per tool. This package owns JSON Schema compilation
// and caching so generated registry specs expose only the stable Validate
// boundary and do not leak the concrete validation library to callers.
package schema

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type validator struct {
	mu       sync.RWMutex
	compiled map[string]*jsonschema.Schema
}

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
// cache. The input data is normalized through JSON so Go numeric/map shapes match
// the JSON document model used by the registry wire contract.
func (v *validator) Validate(schemaBytes []byte, data any, context string) error {
	schema, err := v.compiledSchema(schemaBytes)
	if err != nil {
		return fmt.Errorf("compile %s schema: %w", context, err)
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s for validation: %w", context, err)
	}

	var doc any
	if err := json.Unmarshal(jsonData, &doc); err != nil {
		return fmt.Errorf("parse %s data: %w", context, err)
	}

	if err := schema.Validate(doc); err != nil {
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

	digest := schemaDigest(schemaBytes)

	v.mu.RLock()
	schema := v.compiled[digest]
	v.mu.RUnlock()
	if schema != nil {
		return schema, nil
	}

	var schemaDoc any
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		return nil, fmt.Errorf("unmarshal schema: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	resource := schemaResource(digest)
	if err := compiler.AddResource(resource, schemaDoc); err != nil {
		return nil, fmt.Errorf("add schema resource: %w", err)
	}
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

func schemaDigest(schemaBytes []byte) string {
	sum := sha256.Sum256(schemaBytes)
	return fmt.Sprintf("%x", sum)
}

func schemaResource(digest string) string {
	return "schema://toolregistry/" + digest + ".json"
}
