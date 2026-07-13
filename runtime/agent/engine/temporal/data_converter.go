package temporal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"goa.design/goa-ai/runtime/agent/planner"
)

type (
	// agentJSONPayloadConverter rejects runtime-only values that have no
	// workflow-safe representation, then delegates canonical payloads directly
	// to Temporal's JSON converter.
	//
	// Temporal's default JSON converter decodes `any` fields as JSON-shaped values.
	// Tool results and artifacts therefore cross workflow boundaries as canonical
	// JSON bytes (api.ToolEvent / api.ToolArtifact), not planner.ToolResult.
	//
	// This converter fails fast when code attempts to send planner.ToolResult
	// across a Temporal boundary; callers must use api.ToolEvent.
	agentJSONPayloadConverter struct {
		*converter.JSONPayloadConverter
	}
)

// NewAgentDataConverter returns a Temporal data converter that enforces goa-ai
// workflow boundary contracts.
//
// Tool values use canonical JSON bytes and generated codecs rather than
// interface-valued planner.ToolResult payloads. Other JSON-shaped metadata is
// decoded with json.Number so numeric values remain lossless.
//
// This converter:
//   - Provides stable encoding/decoding for goa-ai API envelopes.
//   - Fails fast if planner.ToolResult crosses a Temporal boundary (use
//     api.ToolEvent instead).
func NewAgentDataConverter() converter.DataConverter {
	base := converter.NewJSONPayloadConverter()
	return converter.NewCompositeDataConverter(
		converter.NewNilPayloadConverter(),
		converter.NewByteSlicePayloadConverter(),
		converter.NewProtoPayloadConverter(),
		converter.NewProtoJSONPayloadConverter(),
		&agentJSONPayloadConverter{
			JSONPayloadConverter: base,
		},
	)
}

func (c *agentJSONPayloadConverter) ToPayload(value any) (*commonpb.Payload, error) {
	switch value.(type) {
	case *planner.ToolResult, planner.ToolResult:
		return nil, fmt.Errorf("temporal: planner.ToolResult must not cross workflow boundaries (use api.ToolEvent)")
	default:
		return c.JSONPayloadConverter.ToPayload(value)
	}
}

func (c *agentJSONPayloadConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
	if payload == nil {
		return fmt.Errorf("temporal: payload is nil")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload.Data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(valuePtr); err != nil {
		return fmt.Errorf("temporal: decode canonical JSON payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("temporal: canonical JSON payload has trailing data")
	}
	return nil
}
