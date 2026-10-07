package temporal

// These tests cross the real private wire converter and compare the facts
// runtime code uses, including empty causes and wrapped cancellation sentinels.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestProviderRecoveryWirePreservesProviderAndCauses(t *testing.T) {
	for _, cause := range []error{
		nil, errors.New(""), errors.New("réseau 世界"),
		fmt.Errorf("canceled phase: %w", context.Canceled),
		fmt.Errorf("expired phase: %w", context.DeadlineExceeded),
		errors.Join(engine.ErrPlannerActivityDeadlineExceeded, context.DeadlineExceeded),
	} {
		provider := model.NewProviderError("synthetic", "complete", 503,
			model.ProviderErrorKindUnavailable, "capacity", "", "request-42", true, cause)
		input := engine.ProviderRecoveryFailure{
			PublicationBatchID: "publication", StartedAt: time.Unix(1, 0), EndedAt: time.Unix(2, 0),
			Err: provider,
		}
		wire, err := encodeRecoveryFailure(input)
		require.NoError(t, err)
		payload, err := NewAgentDataConverter().ToPayload(wire)
		require.NoError(t, err)
		var copy recoveryFailure
		require.NoError(t, NewAgentDataConverter().FromPayload(payload, &copy))
		decoded, err := copy.decode()
		require.NoError(t, err)
		actual := decoded.Err
		assert.Equal(t, provider.Provider(), actual.Provider())
		assert.Equal(t, provider.Operation(), actual.Operation())
		assert.Equal(t, provider.HTTPStatus(), actual.HTTPStatus())
		assert.Equal(t, provider.Kind(), actual.Kind())
		assert.Equal(t, provider.Code(), actual.Code())
		assert.Equal(t, provider.Message(), actual.Message())
		assert.Equal(t, provider.RequestID(), actual.RequestID())
		assert.Equal(t, provider.Retryable(), actual.Retryable())
		assert.Equal(t, provider.Error(), actual.Error())
		if cause == nil {
			require.NoError(t, actual.Unwrap())
		} else {
			require.Error(t, actual.Unwrap())
			assert.Equal(t, cause.Error(), actual.Unwrap().Error())
		}
		for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded, engine.ErrPlannerActivityDeadlineExceeded} {
			assert.Equal(t, errors.Is(provider, sentinel), errors.Is(actual, sentinel))
		}
	}
}

func TestProviderRecoveryWireTransitions(t *testing.T) {
	start, end := time.Unix(1, 0).UTC(), time.Unix(2, 0).UTC()
	provider := model.NewProviderError("synthetic", "complete", 429,
		model.ProviderErrorKindRateLimited, "rate", "wait", "request", true, context.DeadlineExceeded)
	failure := engine.ProviderRecoveryFailure{PublicationBatchID: "publication", StartedAt: start, EndedAt: end, Err: provider}
	for _, message := range []engine.ProviderRecoveryMessage{
		engine.ProviderWaitRequest{Delay: time.Second},
		engine.ProviderAttemptRequest{ActiveDeadline: end},
		engine.ProviderRecoveryPermission{ExpiresAt: end},
		engine.ProviderWaitObserved{StartedAt: start, Through: end},
		engine.ProviderWaitFinished{StartedAt: start, EndedAt: end},
		engine.ProviderWaitObservationRequest{}, engine.ProviderAttemptSucceeded{},
		engine.ProviderAttemptFailed{Failure: failure}, engine.ProviderPhaseUnused{},
		engine.ProviderPhaseFailed{Err: errors.Join(context.Canceled, engine.ErrPlannerActivityDeadlineExceeded)},
		engine.ProviderRecoverySettled{},
		engine.ProviderRecoveryStopped{Err: fmt.Errorf("owner stopped: %w", provider)},
	} {
		t.Run(fmt.Sprintf("%T", message), func(t *testing.T) {
			wire, err := encodeRecoveryMessage(message)
			require.NoError(t, err)
			payload, err := NewAgentDataConverter().ToPayload(wire)
			require.NoError(t, err)
			var copy recoveryMessage
			require.NoError(t, NewAgentDataConverter().FromPayload(payload, &copy))
			decoded, err := copy.decode()
			require.NoError(t, err)
			roundTrip, err := encodeRecoveryMessage(decoded)
			require.NoError(t, err)
			assert.Equal(t, wire, roundTrip)
		})
	}
	_, err := (&recoveryMessage{Kind: "settled", Delay: time.Second}).decode()
	require.ErrorContains(t, err, "outside its variant")
	_, err = (&recoveryMessage{Kind: "stopped"}).decode()
	require.ErrorContains(t, err, "requires an error")
}

func TestProviderRecoveryBindingAcceptsLaterAttemptWithoutReplacingOrigin(t *testing.T) {
	parent := recoveryAddress{Namespace: "test", WorkflowID: "parent", RunID: "parent-run"}
	child := &recoveryChild{
		id: "child", binding: recoveryBinding{Capability: "issued", Parent: parent}, bound: true,
	}
	child.execution.ID, child.execution.RunID = "child", "attempt-1"
	control := &workflowControl{children: map[string]*recoveryChild{"issued": child}}
	for _, current := range []string{"attempt-3", "attempt-1"} {
		frame := recoveryFrame{
			Capability: "issued", ToParent: true, FirstRunID: "attempt-1",
			Source: recoveryAddress{Namespace: "test", WorkflowID: "child", RunID: current},
		}
		actual, ready, err := control.validateRecoverySource(frame)
		require.NoError(t, err)
		assert.True(t, ready)
		assert.Same(t, child, actual)
		assert.Equal(t, current, frame.Source.RunID)
		assert.Equal(t, "attempt-1", child.execution.RunID)
		frame.FirstRunID = "another-chain"
		_, _, err = control.validateRecoverySource(frame)
		assert.ErrorContains(t, err, "accepted child chain")
	}
}
