// Nil targets preserve the cancellation bytes already used by unversioned
// callers. Exact targets change request identity without changing the job ID.
package startrecipe

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestCancellationTargetPreservesUnversionedRecipe(t *testing.T) {
	request := api.CancellationRequest{RunID: "root", Reason: "user_requested"}
	id, unversioned, err := CancellationRecipe("cleanup", "queue", request, nil)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256([]byte(`{"Name":"cleanup","Queue":"queue","Request":{"RunID":"root","Reason":"user_requested"}}`)), unversioned)
	managedID, managed, err := CancellationRecipe("cleanup", "queue", request, &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"})
	require.NoError(t, err)
	assert.Equal(t, id, managedID)
	assert.NotEqual(t, unversioned, managed)
	changedID, changed, err := CancellationRecipe("cleanup", "queue", request, &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-b"})
	require.NoError(t, err)
	assert.Equal(t, id, changedID)
	assert.NotEqual(t, managed, changed)
}

func TestCancellationTargetRequiresCompleteIdentity(t *testing.T) {
	for _, target := range []*engine.WorkerDeploymentVersion{
		{}, {DeploymentName: "workers"}, {BuildID: "build-a"}, {DeploymentName: "workers", BuildID: string([]byte{0xff})},
	} {
		_, _, err := CancellationRecipe("cleanup", "queue", api.CancellationRequest{RunID: "root", Reason: "user_requested"}, target)
		assert.Error(t, err)
	}
}
