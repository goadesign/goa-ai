// Package contract shares the registry's declaration fingerprint with dynamic
// providers. Code generation and this function use the same identity algorithm.
package contract

import (
	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// Fingerprint returns the registry identity of a toolset declaration, including
// its annotations and native Agent targets. RegisteredAt is excluded. Dynamic
// providers use this value in RegisterPayload.SchemaFingerprint; schema and
// execution validation still belong to registration.
func Fingerprint(toolset *genregistry.Toolset) (string, error) {
	return internaladmission.ToolsetFingerprint(toolset)
}
