// Package admission derives declaration identities for current writers and saved records.
// Saved records keep their original contract bytes so added generated defaults
// cannot change an already accepted registration's identity.
package admission

import (
	"encoding/json"
	"fmt"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// ToolsetFingerprint encodes a current typed declaration and returns the same
// identity used by generated registrations and dynamic providers.
func ToolsetFingerprint(toolset *genregistry.Toolset) (string, error) {
	return declarationFingerprint(toolset, nil)
}

// declarationFingerprint combines typed schema fields with either saved contract
// bytes or current generated encoding and returns one registration identity.
func declarationFingerprint(toolset *genregistry.Toolset, contracts []json.RawMessage) (string, error) {
	if toolset == nil {
		return "", fmt.Errorf("toolset declaration is required")
	}
	tools := make([]ToolSchema, len(toolset.Tools))
	for i, tool := range toolset.Tools {
		if tool == nil {
			return "", fmt.Errorf("toolset %q contains a nil tool declaration", toolset.Name)
		}
		var consumerContract []byte
		if contracts != nil {
			consumerContract = contracts[i]
		} else if tool.ConsumerContract != nil {
			var err error
			consumerContract, err = json.Marshal(tool.ConsumerContract)
			if err != nil {
				return "", fmt.Errorf("encode tool %q consumer contract: %w", tool.Name, err)
			}
		}
		tools[i] = ToolSchema{
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
	return SchemaFingerprint(Schema{
		Name:        toolset.Name,
		Description: toolset.Description,
		Version:     version,
		Tags:        toolset.Tags,
		Tools:       tools,
	}), nil
}
