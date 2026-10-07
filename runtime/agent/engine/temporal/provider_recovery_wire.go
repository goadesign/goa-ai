// Package temporal carries provider recovery through private Temporal records.
// These records preserve provider facts and error classifications without
// exposing execution addresses or delivery sequences in the engine interface.
package temporal

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	recoveryTime struct {
		Seconds     int64
		Nanoseconds int32
	}

	recoveryPauseInterval struct {
		StartedAt recoveryTime
		Through   recoveryTime
	}

	recoveryAddress struct {
		Namespace  string
		WorkflowID string
		RunID      string
	}

	recoveryBinding struct {
		Capability string
		Parent     recoveryAddress
		Inherited  bool
	}

	recoveryRequestID struct {
		WorkflowID  string
		RunID       string
		Publication string
	}

	recoveryFrame struct {
		Capability string
		Source     recoveryAddress
		FirstRunID string
		ToParent   bool
		Sequence   uint64
		Ack        bool
		Request    recoveryRequestID
		Open       *recoveryFailure
		Message    *recoveryMessage
		Pause      *recoveryPauseInterval
		Error      *recoveryErrorRecord
	}

	recoveryFailure struct {
		PublicationBatchID string
		StartedAt          recoveryTime
		EndedAt            recoveryTime
		Provider           recoveryProvider
	}

	recoveryProvider struct {
		Provider  string
		Operation string
		HTTP      int
		Kind      model.ProviderErrorKind
		Code      string
		Message   string
		RequestID string
		Retryable bool
		Cause     *recoveryErrorRecord
	}

	recoveryErrorRecord struct {
		Text            string
		Canceled        bool
		Deadline        bool
		PlannerDeadline bool
		Provider        *recoveryProvider
	}

	recoveryWireError struct {
		record recoveryErrorRecord
		cause  error
	}

	recoveryMessage struct {
		Kind    string
		Delay   time.Duration
		At      recoveryTime
		Through recoveryTime
		Failure *recoveryFailure
		Error   *recoveryErrorRecord
	}
)

const (
	recoveryPhaseFailedKind = "phase_failed"
	recoveryStoppedKind     = "stopped"
)

func (e *recoveryWireError) Error() string {
	return e.record.Text
}

func (e *recoveryWireError) Unwrap() error {
	return e.cause
}

func (e *recoveryWireError) Is(target error) bool {
	return target == context.Canceled && e.record.Canceled ||
		target == context.DeadlineExceeded && e.record.Deadline ||
		target == engine.ErrPlannerActivityDeadlineExceeded && e.record.PlannerDeadline
}

// encodeRecoveryError retains the complete diagnostic and the classifications
// callers use. A present cause with empty text remains distinct from no cause.
func encodeRecoveryError(err error) *recoveryErrorRecord {
	if err == nil {
		return nil
	}
	record := &recoveryErrorRecord{
		Text:            err.Error(),
		Canceled:        errors.Is(err, context.Canceled),
		Deadline:        errors.Is(err, context.DeadlineExceeded),
		PlannerDeadline: errors.Is(err, engine.ErrPlannerActivityDeadlineExceeded),
	}
	var provider *model.ProviderError
	if errors.As(err, &provider) {
		value := encodeRecoveryProvider(provider)
		record.Provider = &value
	}
	return record
}

func encodeRecoveryProvider(err *model.ProviderError) recoveryProvider {
	return recoveryProvider{
		Provider: err.Provider(), Operation: err.Operation(), HTTP: err.HTTPStatus(),
		Kind: err.Kind(), Code: err.Code(), Message: err.Message(),
		RequestID: err.RequestID(), Retryable: err.Retryable(),
		Cause: encodeRecoveryError(err.Unwrap()),
	}
}

func encodeRecoveryFailure(failure engine.ProviderRecoveryFailure) (*recoveryFailure, error) {
	if failure.Err == nil || failure.PublicationBatchID == "" {
		return nil, errors.New("provider recovery requires a published provider failure")
	}
	return &recoveryFailure{
		PublicationBatchID: failure.PublicationBatchID,
		StartedAt:          encodeRecoveryTime(failure.StartedAt),
		EndedAt:            encodeRecoveryTime(failure.EndedAt),
		Provider:           encodeRecoveryProvider(failure.Err),
	}, nil
}

// encodeRecoveryMessage translates only the closed engine message set. Numeric
// allowances, time coverage, and permission decisions remain runtime concerns.
func encodeRecoveryMessage(message engine.ProviderRecoveryMessage) (*recoveryMessage, error) {
	out := &recoveryMessage{}
	switch value := message.(type) {
	case engine.ProviderWaitRequest:
		out.Kind, out.Delay = "wait", value.Delay
	case engine.ProviderAttemptRequest:
		out.Kind, out.At = "attempt", encodeRecoveryTime(value.ActiveDeadline)
	case engine.ProviderRecoveryPermission:
		out.Kind, out.At = "permission", encodeRecoveryTime(value.ExpiresAt)
	case engine.ProviderWaitObserved:
		out.Kind, out.At, out.Through = "observed", encodeRecoveryTime(value.StartedAt), encodeRecoveryTime(value.Through)
	case engine.ProviderWaitFinished:
		out.Kind, out.At, out.Through = "finished", encodeRecoveryTime(value.StartedAt), encodeRecoveryTime(value.EndedAt)
	case engine.ProviderWaitObservationRequest:
		out.Kind = "observe"
	case engine.ProviderAttemptSucceeded:
		out.Kind = "succeeded"
	case engine.ProviderAttemptFailed:
		failure, err := encodeRecoveryFailure(value.Failure)
		if err != nil {
			return nil, err
		}
		out.Kind, out.Failure = "failed", failure
	case engine.ProviderPhaseUnused:
		out.Kind = "unused"
	case engine.ProviderPhaseFailed:
		out.Kind, out.Error = recoveryPhaseFailedKind, encodeRecoveryError(value.Err)
	case engine.ProviderRecoverySettled:
		out.Kind = "settled"
	case engine.ProviderRecoveryStopped:
		out.Kind, out.Error = recoveryStoppedKind, encodeRecoveryError(value.Err)
	default:
		return nil, fmt.Errorf("unsupported provider recovery message %T", message)
	}
	if (out.Kind == recoveryPhaseFailedKind || out.Kind == recoveryStoppedKind) && out.Error == nil {
		return nil, errors.New("provider recovery terminal message requires an error")
	}
	return out, nil
}

func (r *recoveryErrorRecord) decode() (error, error) {
	if r == nil {
		return nil, nil
	}
	out := &recoveryWireError{record: *r}
	if r.Provider != nil {
		provider, err := r.Provider.decode()
		if err != nil {
			return nil, err
		}
		out.cause = provider
	}
	return out, nil
}

func (r recoveryProvider) decode() (*model.ProviderError, error) {
	if r.Provider == "" || r.Kind == "" {
		return nil, errors.New("provider recovery wire error has no provider or kind")
	}
	cause, err := r.Cause.decode()
	if err != nil {
		return nil, err
	}
	return model.NewProviderError(r.Provider, r.Operation, r.HTTP, r.Kind,
		r.Code, r.Message, r.RequestID, r.Retryable, cause), nil
}

func (r *recoveryFailure) decode() (engine.ProviderRecoveryFailure, error) {
	if r == nil || r.PublicationBatchID == "" {
		return engine.ProviderRecoveryFailure{}, errors.New("provider recovery wire failure has no publication")
	}
	provider, err := r.Provider.decode()
	if err != nil {
		return engine.ProviderRecoveryFailure{}, err
	}
	if encodeRecoveryTime(r.StartedAt.time()) != r.StartedAt || encodeRecoveryTime(r.EndedAt.time()) != r.EndedAt {
		return engine.ProviderRecoveryFailure{}, errors.New("provider recovery failure has an invalid timestamp")
	}
	return engine.ProviderRecoveryFailure{
		PublicationBatchID: r.PublicationBatchID,
		StartedAt:          r.StartedAt.time(),
		EndedAt:            r.EndedAt.time(),
		Err:                provider,
	}, nil
}

// decode rejects unknown variants and fields belonging to another variant.
// It does not repair a malformed transport record into a valid runtime message.
func (r *recoveryMessage) decode() (engine.ProviderRecoveryMessage, error) {
	var message engine.ProviderRecoveryMessage
	switch r.Kind {
	case "wait":
		message = engine.ProviderWaitRequest{Delay: r.Delay}
	case "attempt":
		message = engine.ProviderAttemptRequest{ActiveDeadline: r.At.time()}
	case "permission":
		message = engine.ProviderRecoveryPermission{ExpiresAt: r.At.time()}
	case "observed":
		message = engine.ProviderWaitObserved{StartedAt: r.At.time(), Through: r.Through.time()}
	case "finished":
		message = engine.ProviderWaitFinished{StartedAt: r.At.time(), EndedAt: r.Through.time()}
	case "observe":
		message = engine.ProviderWaitObservationRequest{}
	case "succeeded":
		message = engine.ProviderAttemptSucceeded{}
	case "failed":
		failure, err := r.Failure.decode()
		if err != nil {
			return nil, err
		}
		message = engine.ProviderAttemptFailed{Failure: failure}
	case "unused":
		message = engine.ProviderPhaseUnused{}
	case recoveryPhaseFailedKind, recoveryStoppedKind:
		cause, err := r.Error.decode()
		if err != nil {
			return nil, err
		}
		if r.Kind == recoveryStoppedKind {
			message = engine.ProviderRecoveryStopped{Err: cause}
		} else {
			message = engine.ProviderPhaseFailed{Err: cause}
		}
	case "settled":
		message = engine.ProviderRecoverySettled{}
	default:
		return nil, fmt.Errorf("unknown provider recovery wire message %q", r.Kind)
	}
	canonical, err := encodeRecoveryMessage(message)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(r, canonical) {
		return nil, errors.New("provider recovery message contains fields outside its variant")
	}
	return message, nil
}

// encodeRecoveryTime carries the instant without a custom JSON marshaler.
// Unix seconds preserve a zero Go time as well as ordinary workflow timestamps.
func encodeRecoveryTime(value time.Time) recoveryTime {
	nanoseconds := int32(value.Nanosecond()) //nolint:gosec // G115: time.Time.Nanosecond returns [0, 999999999]; int32 preserves that offset exactly.
	return recoveryTime{Seconds: value.Unix(), Nanoseconds: nanoseconds}
}

func (r recoveryTime) time() time.Time {
	return time.Unix(r.Seconds, int64(r.Nanoseconds)).UTC()
}
