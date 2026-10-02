// Package bedrock checks the resolved model and authored tool choice before
// inference or counting. Known endpoint restrictions return a local request
// error without changing the choice or inventing an AWS response.
package bedrock

import (
	"fmt"

	"goa.design/goa-ai/features/model/internal/claudecaps"
	"goa.design/goa-ai/runtime/agent/model"
)

// validateBedrockForcedToolChoice rejects only Bedrock's additional restriction.
// Existing model-wide rejections remain with their original encoder and error.
func validateBedrockForcedToolChoice(modelID string, choice *model.ToolChoice) error {
	if forcesToolUse(choice) &&
		!claudecaps.ForcedToolChoiceUnsupported(modelID) &&
		claudecaps.BedrockForcedToolChoiceUnsupported(modelID) {
		return model.NewRequestValidationError(fmt.Errorf(
			"bedrock: model %q does not support forced tool choice mode %q",
			modelID, choice.Mode,
		))
	}
	return nil
}
