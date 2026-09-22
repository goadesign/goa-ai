// Package contract shares the registry's declaration fingerprint with dynamic
// providers. Code generation and this function use the same identity algorithm.
package contract

import (
	"encoding/json"
	"fmt"

	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// Fingerprint returns the registry identity of a toolset declaration, including
// its annotations and native Agent targets. RegisteredAt is excluded. Dynamic
// providers use this value in RegisterPayload.SchemaFingerprint; schema and
// execution validation still belong to registration.
func Fingerprint(toolset *genregistry.Toolset) (string, error) {
	if toolset == nil {
		return "", fmt.Errorf("toolset declaration is required")
	}
	tools := make([]internaladmission.ToolSchema, len(toolset.Tools))
	for i, tool := range toolset.Tools {
		if tool == nil {
			return "", fmt.Errorf("toolset %q contains a nil tool declaration", toolset.Name)
		}
		var consumerContract []byte
		if tool.ConsumerContract != nil {
			var err error
			consumerContract, err = json.Marshal(tool.ConsumerContract)
			if err != nil {
				return "", fmt.Errorf("encode tool %q consumer contract: %w", tool.Name, err)
			}
		}
		tools[i] = internaladmission.ToolSchema{
			Name:                   tool.Name,
			Description:            tool.Description,
			Tags:                   tool.Tags,
			PayloadSchema:          tool.PayloadSchema,
			ExecutionPayloadSchema: tool.ExecutionPayloadSchema,
			ResultSchema:           tool.ResultSchema,
			SidecarSchema:          tool.SidecarSchema,
			ConsumerContract:       consumerContract,
		}
	}
	var version *string
	if toolset.Version != nil {
		value := string(*toolset.Version)
		version = &value
	}
	return internaladmission.SchemaFingerprint(internaladmission.Schema{
		Name:        toolset.Name,
		Description: toolset.Description,
		Version:     version,
		Tags:        toolset.Tags,
		Tools:       tools,
	}), nil
}
