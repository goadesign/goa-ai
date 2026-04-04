// Package telemetry classifies operation outcomes for generic runtime tracing.
//
// Contract:
//   - Tracing code records a span failure for any non-nil error unless the
//     active context is already done and the returned error is a supported
//     context-termination shape.
//   - The classifier is transport-aware for generic context termination forms:
//     raw context sentinels plus gRPC canceled/deadline-exceeded statuses.
//   - The classifier is runtime-generic. It does not know about any
//     application-specific error taxonomies, dashboards, or product semantics.
package telemetry

import (
	"context"
	"errors"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
)

// ShouldRecordSpanError reports whether err should mark the current span as a
// failure.
//
// A non-nil error is not recorded as a span failure only when the active
// context is already canceled or timed out and the returned error matches one
// of the supported context-termination shapes.
func ShouldRecordSpanError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return !isContextTerminationError(ctx, err)
}

// isContextTerminationError reports whether err is explained by the active
// context already being done.
func isContextTerminationError(ctx context.Context, err error) bool {
	if ctx == nil || err == nil {
		return false
	}
	if ctx.Err() == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	code := grpcStatus.Code(err)
	return code == grpcCodes.Canceled || code == grpcCodes.DeadlineExceeded
}
