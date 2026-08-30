package startrecipe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestDigestFramesMapEntriesAndIgnoresMapOrder(t *testing.T) {
	dataConverter := NewDataConverter()
	inputSnapshot, err := SnapshotRunInput(dataConverter, &api.RunInput{RunID: "run-1"})
	require.NoError(t, err)
	base := DigestInput{
		Workflow: "agent.workflow", TaskQueue: "agent.queue", InputPayload: inputSnapshot.Payload,
	}

	first := base
	first.Memo = map[string]any{"a": "bc", "order": "stable"}
	firstDigest, err := Digest(dataConverter, first)
	require.NoError(t, err)
	same := base
	same.Memo = map[string]any{"order": "stable", "a": "bc"}
	sameDigest, err := Digest(dataConverter, same)
	require.NoError(t, err)
	require.Equal(t, firstDigest, sameDigest)

	ambiguousWithoutFraming := base
	ambiguousWithoutFraming.Memo = map[string]any{"ab": "c", "order": "stable"}
	ambiguousDigest, err := Digest(dataConverter, ambiguousWithoutFraming)
	require.NoError(t, err)
	require.NotEqual(t, firstDigest, ambiguousDigest)
}

func TestDigestPreservesNativePayloadType(t *testing.T) {
	dataConverter := NewDataConverter()
	inputSnapshot, err := SnapshotRunInput(dataConverter, &api.RunInput{RunID: "run-1"})
	require.NoError(t, err)
	base := DigestInput{
		Workflow: "agent.workflow", TaskQueue: "agent.queue", InputPayload: inputSnapshot.Payload,
	}
	binaryMemo := base
	binaryMemo.Memo = map[string]any{"value": []byte("same bytes")}
	binaryDigest, err := Digest(dataConverter, binaryMemo)
	require.NoError(t, err)
	stringMemo := base
	stringMemo.Memo = map[string]any{"value": "same bytes"}
	stringDigest, err := Digest(dataConverter, stringMemo)
	require.NoError(t, err)
	require.NotEqual(t, binaryDigest, stringDigest)
}

func TestDigestBindsEachSubmittedStartComponent(t *testing.T) {
	dataConverter := NewDataConverter()
	inputSnapshot, err := SnapshotRunInput(dataConverter, &api.RunInput{RunID: "run-1"})
	require.NoError(t, err)
	searchAttributes, err := EncodeSearchAttributes(map[string]any{"site": "one"})
	require.NoError(t, err)
	base := DigestInput{
		Workflow:         "workflow",
		TaskQueue:        "queue",
		InputPayload:     inputSnapshot.Payload,
		RunTimeout:       time.Minute,
		RetryPolicy:      engine.RetryPolicy{MaxAttempts: 2, InitialInterval: time.Second, BackoffCoefficient: 2},
		Memo:             map[string]any{"memo": "one"},
		SearchAttributes: searchAttributes,
	}
	baseDigest, err := Digest(dataConverter, base)
	require.NoError(t, err)

	changedInput, err := SnapshotRunInput(dataConverter, &api.RunInput{RunID: "run-2"})
	require.NoError(t, err)
	changedSearch, err := EncodeSearchAttributes(map[string]any{"site": "two"})
	require.NoError(t, err)
	variants := []DigestInput{
		func() DigestInput { value := base; value.Workflow = "other"; return value }(),
		func() DigestInput { value := base; value.TaskQueue = "other"; return value }(),
		func() DigestInput { value := base; value.InputPayload = changedInput.Payload; return value }(),
		func() DigestInput { value := base; value.RunTimeout = 2 * time.Minute; return value }(),
		func() DigestInput {
			value := base
			value.RetryPolicy = engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Second, BackoffCoefficient: 2}
			return value
		}(),
		func() DigestInput { value := base; value.Memo = map[string]any{"memo": "two"}; return value }(),
		func() DigestInput { value := base; value.SearchAttributes = changedSearch; return value }(),
	}
	for index, variant := range variants {
		digest, err := Digest(dataConverter, variant)
		require.NoError(t, err)
		require.NotEqual(t, baseDigest, digest, "variant %d", index)
	}
}
