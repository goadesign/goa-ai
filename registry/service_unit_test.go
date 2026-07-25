package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestToolUseIDForCallUsesRequiredRunScopedIdentity(t *testing.T) {
	t.Parallel()

	callID := "model-call-1"
	meta := &genregistry.ToolCallMeta{
		RunID:      "run-1",
		SessionID:  "session-1",
		ToolCallID: callID,
	}
	assert.Equal(
		t,
		toolregistry.DeriveToolUseID(meta.RunID, callID),
		toolUseIDForCall(meta),
	)
	otherRun := *meta
	otherRun.RunID = "run-2"
	assert.NotEqual(t, toolUseIDForCall(meta), toolUseIDForCall(&otherRun))
}
