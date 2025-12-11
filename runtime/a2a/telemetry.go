package a2a

import (
	"encoding/json"

	"goa.design/goa-ai/runtime/agent/telemetry"
)

// ExtractTelemetry constructs ToolTelemetry from a SendTaskResponse. It
// interprets the Structured field as an opaque JSON value and attaches it
// to the Extra map for downstream sinks. Duration, model, and token fields
// are left to higher-level integrations.
func ExtractTelemetry(resp SendTaskResponse) *telemetry.ToolTelemetry {
	if len(resp.Structured) == 0 {
		return nil
	}
	var structured any
	if err := json.Unmarshal(resp.Structured, &structured); err != nil {
		return nil
	}
	return &telemetry.ToolTelemetry{
		Extra: map[string]any{"structured": structured},
	}
}


