// Package registrycall validates generated registry admissions before the
// executor opens a result stream. Deadlines come from the registry response;
// malformed identities or timestamps leave the provider outcome unknown.
package registrycall

import (
	"fmt"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

// decodeAdmission checks the registry's two independent timestamps and call
// identity. Only a valid response supplies the executor's waiting deadlines.
func decodeAdmission(result *genregistry.CallToolResult) (toolregistry.ToolCallRef, error) {
	deadline, err := time.Parse(time.RFC3339Nano, result.ExecutionDeadline)
	if err != nil {
		return toolregistry.ToolCallRef{}, fmt.Errorf("registry execution deadline: %w", err)
	}
	expiration, err := time.Parse(time.RFC3339Nano, result.ResultStreamExpiresAt)
	if err != nil {
		return toolregistry.ToolCallRef{}, fmt.Errorf("registry result expiration: %w", err)
	}
	ref := toolregistry.ToolCallRef{
		ToolUseID:             result.ToolUseID,
		RegistrationToken:     result.RegistrationToken,
		ExecutionDeadline:     deadline,
		ResultStreamExpiresAt: expiration,
	}
	if err := toolregistry.ValidateToolCallRef(ref); err != nil {
		return toolregistry.ToolCallRef{}, err
	}
	return ref, nil
}
