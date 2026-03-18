// Package registry centralizes schema admission and payload validation.
// Registration compiles every admitted tool schema up front, and tool calls
// reuse the same compiled-schema cache so live traffic does not rediscover
// invalid schemas or repeatedly pay compilation cost for the same contract.
package registry

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type (
	// schemaValidator owns compiled JSON Schemas for registry admission and
	// runtime payload validation. Cache entries are keyed by schema digest so
	// repeated registrations and tool calls share one compiled contract.
	schemaValidator struct {
		mu       sync.RWMutex
		compiled map[string]*jsonschema.Schema
	}
)

func newSchemaValidator() *schemaValidator {
	return &schemaValidator{
		compiled: make(map[string]*jsonschema.Schema),
	}
}

// ValidateToolSchemas enforces the registration contract for every tool schema
// the registry admits. Payload and result schemas are required; sidecar schema
// is optional but must compile when present.
func (v *schemaValidator) ValidateToolSchemas(tools []*genregistry.ToolSchema) error {
	for _, tool := range tools {
		if tool == nil {
			return fmt.Errorf("tool schema is nil")
		}
		if tool.Name == "" {
			return fmt.Errorf("tool schema missing name")
		}
		if err := v.validateRequiredSchema(tool.Name, "payload", tool.PayloadSchema); err != nil {
			return err
		}
		if err := v.validateRequiredSchema(tool.Name, "result", tool.ResultSchema); err != nil {
			return err
		}
		if err := v.validateOptionalSchema(tool.Name, "sidecar", tool.SidecarSchema); err != nil {
			return err
		}
	}
	return nil
}

// ValidatePayload validates the raw tool-call payload against the compiled
// schema for the target tool. Missing payload schemas are rejected so corrupted
// catalog entries fail fast instead of silently bypassing validation.
func (v *schemaValidator) ValidatePayload(schemaBytes []byte, payloadJSON []byte) error {
	if len(schemaBytes) == 0 {
		return fmt.Errorf("payload schema is required")
	}

	schema, err := v.compiledSchema(schemaBytes)
	if err != nil {
		return err
	}

	var payloadDoc any
	if err := json.Unmarshal(payloadJSON, &payloadDoc); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	if err := schema.Validate(payloadDoc); err != nil {
		return err
	}
	return nil
}

// validateRequiredSchema compiles a required schema and reports the registry
// field that violated admission.
func (v *schemaValidator) validateRequiredSchema(toolName string, schemaName string, schemaBytes []byte) error {
	if len(schemaBytes) == 0 {
		return fmt.Errorf("tool %q: %s schema is required", toolName, schemaName)
	}
	if _, err := v.compiledSchema(schemaBytes); err != nil {
		return fmt.Errorf("tool %q: %s schema: %w", toolName, schemaName, err)
	}
	return nil
}

// validateOptionalSchema compiles an optional schema only when the producer
// supplied one.
func (v *schemaValidator) validateOptionalSchema(toolName string, schemaName string, schemaBytes []byte) error {
	if len(schemaBytes) == 0 {
		return nil
	}
	if _, err := v.compiledSchema(schemaBytes); err != nil {
		return fmt.Errorf("tool %q: %s schema: %w", toolName, schemaName, err)
	}
	return nil
}

// compiledSchema returns the compiled form of a schema, creating and caching it
// on first use. The cache is per-service because admission and call routing
// share one registry process.
func (v *schemaValidator) compiledSchema(schemaBytes []byte) (*jsonschema.Schema, error) {
	if len(schemaBytes) == 0 {
		return nil, nil
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
	return "schema://registry/" + digest + ".json"
}
