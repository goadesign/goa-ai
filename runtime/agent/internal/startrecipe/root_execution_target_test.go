package startrecipe

// Root worker selection belongs to the accepted engine request. These tests
// keep it immutable and include it in stored and duplicate-start identity.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestRootExecutionTargetSnapshotAndPreparedRoundTrip(t *testing.T) {
	target := &engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: "build-a"}
	request := engine.WorkflowStartRequest{
		ID: "root", Workflow: "agent.workflow", TaskQueue: "agent.queue", RootExecutionTarget: target,
		Input: &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, RunID: "root"},
	}
	prepared, err := NewPreparedRequest("agent", request, "")
	require.NoError(t, err)
	accepted := *target
	target.BuildID = "mutated"
	require.Equal(t, accepted, *prepared.Request.RootExecutionTarget)
	data, err := prepared.MarshalBinary()
	require.NoError(t, err)
	parsed, err := ParsePreparedRequest(data)
	require.NoError(t, err)
	require.Equal(t, accepted, *parsed.Request.RootExecutionTarget)
	require.Equal(t, prepared.Digest, parsed.Digest)
	again, err := parsed.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, data, again)
	for _, changed := range []*engine.WorkerDeploymentVersion{
		nil,
		{DeploymentName: accepted.DeploymentName, BuildID: "build-b"},
		{DeploymentName: "other-execution", BuildID: accepted.BuildID},
	} {
		request.RootExecutionTarget = changed
		snapshot, err := SnapshotRequest(request)
		require.NoError(t, err)
		require.NotEqual(t, prepared.Digest, snapshot.Digest)
	}
}

func TestRootExecutionTargetRejectsIncompleteOrOversizedIdentity(t *testing.T) {
	for _, target := range []*engine.WorkerDeploymentVersion{
		{}, {DeploymentName: "execution"}, {BuildID: "build"},
		{DeploymentName: "execution", BuildID: strings.Repeat("x", engine.MaxPayloadBytes)},
	} {
		request := engine.WorkflowStartRequest{
			ID: "root", Workflow: "agent.workflow", TaskQueue: "agent.queue", RootExecutionTarget: target,
			Input: &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, RunID: "root"},
		}
		_, err := SnapshotRequest(request)
		require.Error(t, err)
	}
}
