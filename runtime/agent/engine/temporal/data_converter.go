// Package temporal exposes the shared workflow codec through the Temporal
// adapter. The codec owns validation, copying, and byte limits for every
// engine.
package temporal

import (
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
)

// NewAgentDataConverter returns the shared workflow data converter installed
// on every Temporal client and worker.
func NewAgentDataConverter() converter.DataConverter {
	return workflowcodec.NewDataConverter()
}
