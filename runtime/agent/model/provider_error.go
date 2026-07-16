package model

import (
	"errors"
	"fmt"
	"net/http"
)

// ProviderErrorKind classifies provider failures into a small set of categories
// suitable for retry and UX decisions.
type ProviderErrorKind string

const (
	// ProviderErrorKindAuth indicates authentication/authorization failures.
	ProviderErrorKindAuth ProviderErrorKind = "auth"

	// ProviderErrorKindInvalidRequest indicates the request is invalid and retrying
	// without changing the request will not succeed.
	ProviderErrorKindInvalidRequest ProviderErrorKind = "invalid_request"

	// ProviderErrorKindRateLimited indicates the provider is throttling requests.
	ProviderErrorKindRateLimited ProviderErrorKind = "rate_limited"

	// ProviderErrorKindUnavailable indicates a transient provider failure (5xx,
	// network issues) where a retry may succeed.
	ProviderErrorKindUnavailable ProviderErrorKind = "unavailable"

	// ProviderErrorKindUnknown indicates an unclassified provider failure.
	ProviderErrorKindUnknown ProviderErrorKind = "unknown"
)

const (
	// providerErrorCodeEmptyStream is the stable code carried by empty-stream
	// provider errors. Detection goes through errors.Is(err, ErrEmptyStream);
	// the code exists for observability (span/error attributes), not matching.
	providerErrorCodeEmptyStream = "empty_stream"

	// providerErrorCodeTruncatedStream is the stable code carried by provider
	// errors for streams that closed mid-message. Like the empty-stream code,
	// it exists for observability, not matching.
	providerErrorCodeTruncatedStream = "truncated_stream"
)

// ProviderError describes a failure returned by a model provider (e.g. Bedrock).
// It is intended to cross package boundaries so runtimes can surface stable,
// structured information to callers.
type ProviderError struct {
	provider  string
	operation string
	http      int
	kind      ProviderErrorKind
	code      string
	message   string
	requestID string
	retryable bool
	cause     error
}

// NewProviderError constructs a ProviderError. provider and kind are required.
// cause may be nil but is recommended to preserve the original error chain.
func NewProviderError(provider, operation string, httpStatus int, kind ProviderErrorKind, code, message, requestID string, retryable bool, cause error) *ProviderError {
	if provider == "" {
		panic("model: provider is required")
	}
	if kind == "" {
		panic("model: provider error kind is required")
	}
	return &ProviderError{
		provider:  provider,
		operation: operation,
		http:      httpStatus,
		kind:      kind,
		code:      code,
		message:   message,
		requestID: requestID,
		retryable: retryable,
		cause:     cause,
	}
}

// Provider returns the provider identifier (for example, "bedrock").
func (e *ProviderError) Provider() string { return e.provider }

// Operation returns the provider operation name when known (for example, "converse_stream").
func (e *ProviderError) Operation() string { return e.operation }

// HTTPStatus returns the provider HTTP status code when available, otherwise 0.
func (e *ProviderError) HTTPStatus() int { return e.http }

// Kind returns the coarse-grained provider error classification.
func (e *ProviderError) Kind() ProviderErrorKind { return e.kind }

// Code returns the provider-specific error code when available.
func (e *ProviderError) Code() string { return e.code }

// Message returns the provider error message when available.
func (e *ProviderError) Message() string { return e.message }

// RequestID returns the provider request identifier when available.
func (e *ProviderError) RequestID() string { return e.requestID }

// Retryable reports whether retrying the call may succeed without changing the request.
func (e *ProviderError) Retryable() bool { return e.retryable }

func (e *ProviderError) Error() string {
	op := e.operation
	if op == "" {
		op = "request"
	}
	status := ""
	if e.http > 0 {
		status = fmt.Sprintf("%d ", e.http)
	}
	code := ""
	if e.code != "" {
		code = e.code + ": "
	}
	msg := e.message
	if msg == "" && e.cause != nil {
		msg = e.cause.Error()
	}
	if msg == "" {
		msg = "provider error"
	}
	return fmt.Sprintf("%s %s %s(%s): %s", e.provider, e.kind, status, op, code+msg)
}

// Unwrap returns the underlying provider error to preserve the original error chain.
func (e *ProviderError) Unwrap() error { return e.cause }

// AsProviderError returns the first ProviderError in err's chain, if any.
func AsProviderError(err error) (*ProviderError, bool) {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// NewEmptyStreamError classifies a streaming response that the provider
// terminated without ever starting an assistant message. Adapters call it
// from their stream event loops when the terminal event (or the end of the
// event stream) arrives before any message started, which providers
// intermittently produce for empty model completions. The result carries
// ErrEmptyStream for errors.Is detection plus a retryable unavailable
// ProviderError so retry middleware and observability see one consistent
// classification. message describes the protocol shape observed (for
// example, "message stop received without an active message").
func NewEmptyStreamError(provider, operation, message string) error {
	pe := NewProviderError(
		provider,
		operation,
		0,
		ProviderErrorKindUnavailable,
		providerErrorCodeEmptyStream,
		message,
		"",
		true,
		nil,
	)
	return errors.Join(ErrEmptyStream, pe)
}

// NewStreamEndedEarlyError classifies an event stream that terminated cleanly
// before the provider's message-stop boundary. Adapters call it from their
// stream event loops when the provider closes the connection without error
// but the message protocol is unfinished. started reports whether an
// assistant message had begun:
//
//   - started=false means the provider produced an empty completion; the
//     result is an empty-stream error (see NewEmptyStreamError) that callers
//     may retry via errors.Is(err, ErrEmptyStream).
//   - started=true means the stream was truncated mid-generation; the result
//     is a retryable unavailable ProviderError (code truncated_stream)
//     without the empty-stream sentinel, so pre-output retry policies never
//     match partially delivered responses.
func NewStreamEndedEarlyError(provider, operation string, started bool) error {
	if !started {
		return NewEmptyStreamError(provider, operation, "stream ended before message start")
	}
	return NewProviderError(
		provider,
		operation,
		0,
		ProviderErrorKindUnavailable,
		providerErrorCodeTruncatedStream,
		"stream ended before message stop",
		"",
		true,
		nil,
	)
}

// ClassifyHTTPStatus maps an HTTP status code returned by a model provider to
// the goa-ai provider error contract. It is the single status-to-kind table
// for adapters that need only status-based classification (Vertex Gemini,
// Vertex/direct Anthropic). Adapters that must also preserve a
// provider-specific error code through ProviderError.Code (Bedrock's smithy
// exception names, OpenAI's error codes) keep their own tables, since this
// classifier does not carry a code.
//
// status is the provider's HTTP status code, or 0 when the adapter could not
// recover one from a non-HTTP error (for example, a network failure or a
// pre-classified sentinel error). cause is preserved as the returned error's
// Unwrap target so errors.Is/As still see the original failure (and any
// sentinel, such as ErrRateLimited, that cause already wraps).
//
// Classification:
//
//   - 429: kind rate_limited, retryable, and the result additionally wraps
//     ErrRateLimited via errors.Join so adaptive rate-limit middleware backs
//     off without inspecting kinds.
//   - 400: kind invalid_request.
//   - 401 or 403: kind auth.
//   - 500-599: kind unavailable, retryable.
//   - anything else (including 0): kind unknown.
func ClassifyHTTPStatus(provider, operation string, status int, message string, cause error) error {
	kind := ProviderErrorKindUnknown
	retryable := false
	switch {
	case status == http.StatusTooManyRequests:
		pe := NewProviderError(provider, operation, status, ProviderErrorKindRateLimited, "", message, "", true, cause)
		return errors.Join(ErrRateLimited, pe)
	case status == http.StatusBadRequest:
		kind = ProviderErrorKindInvalidRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = ProviderErrorKindAuth
	case status >= 500 && status < 600:
		kind = ProviderErrorKindUnavailable
		retryable = true
	}
	return NewProviderError(provider, operation, status, kind, "", message, "", retryable, cause)
}
