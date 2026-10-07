// Package toolregistry validates the workflow-owned input round at registry
// admission and provider delivery. The service still owns its opaque state and
// validates known host answers with generated codecs before using them.
package toolregistry

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	"goa.design/goa-ai/runtime/mcp"
)

// ValidateInputRound checks that initial calls have no continuation and later
// rounds carry host input. It rejects interaction on text-only calls and invalid
// answer JSON before the registry can admit an immutable request.
func ValidateInputRound(round uint64, continuation *mcp.CallContinuation, textOnly bool) error {
	if (round == 0) != (continuation == nil) {
		return fmt.Errorf("input continuation must be absent at round zero and present on later rounds")
	}
	if continuation == nil {
		return nil
	}
	if textOnly {
		return fmt.Errorf("text-only calls cannot carry input continuation")
	}
	for id, answer := range continuation.InputResponses {
		if id == "" {
			return fmt.Errorf("input response ID must not be empty")
		}
		var object map[string]json.RawMessage
		if err := jsonv2.Unmarshal(answer, &object); err != nil {
			return fmt.Errorf("input response %q: %w", id, err)
		}
		if object == nil {
			return fmt.Errorf("input response %q must be one JSON object", id)
		}
	}
	return nil
}
