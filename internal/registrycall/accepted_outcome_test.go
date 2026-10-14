// Registry result history can expire after provider execution. The executor
// must preserve that uncertainty and forbid a replacement operation.
package registrycall

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestAcceptedTransportExpiredHistoryPreservesUnknownOutcome(t *testing.T) {
	err := genregistry.MakeServiceUnavailable(errors.New("accepted sequence result history expired; its outcome is unknown and it will not execute again"))
	result, definite := preAdmissionFailureResult(err)
	assert.False(t, definite)
	assert.Nil(t, result)
	result = outcomeUnknownResult(err)
	require.NotNil(t, result.Failure)
	assert.Equal(t, planner.RecoveryFinish, result.Failure.Recovery.Action)
	assert.Contains(t, result.Failure.Error.Message, toolregistry.ToolErrorCodeOutcomeUnknown)
}

func TestAcceptedTransportRetainedRejectionAllowsReplanning(t *testing.T) {
	err := genregistry.MakeCallNotAdmitted(errors.New("provider unavailable before publication; rejection retained"))
	result, definite := preAdmissionFailureResult(err)
	require.True(t, definite)
	require.NotNil(t, result.Failure)
	assert.Equal(t, planner.RecoveryReplan, result.Failure.Recovery.Action)
}
