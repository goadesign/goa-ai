// Package runtime carries the accepted output restriction to local executors.
// Services receive the same restriction through generated tool-call metadata.
package runtime

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// prepareTextOnlyExecutionPayload checks the selected execution contract and
// encodes its disabled controls before a tool runs. The caller keeps the
// original model arguments separately for conversation history and correction.
func prepareTextOnlyExecutionPayload(spec tools.ToolSpec, payload rawjson.Message) (rawjson.Message, error) {
	if spec.RequiresUI || spec.Confirmation != nil || spec.TextOnly == nil {
		return nil, errors.New("tool requires UI or has no generated text-only contract")
	}
	value, err := spec.TextOnly.ExecutionCodec.FromJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("text-only execution arguments: %w", err)
	}
	encoded, err := spec.TextOnly.ExecutionCodec.ToJSON(value)
	if err != nil {
		return nil, fmt.Errorf("encode text-only execution arguments: %w", err)
	}
	return rawjson.Message(encoded), nil
}
