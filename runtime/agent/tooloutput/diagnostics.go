// Each private Run receives original runtime errors through its tracer before
// workflow records bound their text. A failed Run returns a frozen copy of those
// observations alongside its terminal error; this receiver never controls retries.

package tooloutput

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/trace"

	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	// runDiagnostics owns observations for one Run, including activities that
	// finish after its caller stops waiting. It never exports them globally.
	runDiagnostics struct {
		mu           sync.Mutex
		observations []errorObservation
	}

	// errorObservation retains the actual object delivered by the runtime,
	// which may already be a transported or converted error.
	errorObservation struct {
		operation string
		err       error
	}

	// diagnosticSpan collects errors only. Its embedded no-op span discards
	// attributes, events, status, and timing without changing runtime behavior.
	diagnosticSpan struct {
		telemetry.Span
		receiver  *runDiagnostics
		operation string
	}

	diagnosticSpanKey struct{}

	// diagnosticError renders every cause without replacing the original tree.
	diagnosticError struct {
		text  string
		cause error
	}
)

// Start attaches the current operation to context so nested runtime code can
// record failures without knowing about this private receiver.
func (d *runDiagnostics) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	span := &diagnosticSpan{
		Span:      telemetry.NoopTracer{}.Span(ctx),
		receiver:  d,
		operation: name,
	}
	return context.WithValue(ctx, diagnosticSpanKey{}, span), span
}

// Span uses only an operation attached by this Run; unrelated contexts retain
// the private runtime's existing no-op behavior.
func (d *runDiagnostics) Span(ctx context.Context) telemetry.Span {
	if span, ok := ctx.Value(diagnosticSpanKey{}).(*diagnosticSpan); ok && span.receiver == d {
		return span
	}
	return telemetry.NoopTracer{}.Span(ctx)
}

// RecordError retains the received object unchanged, not its bounded text.
// Runtime callers own which failures are recorded and must not mutate them.
func (s *diagnosticSpan) RecordError(err error, _ ...trace.EventOption) {
	if err == nil {
		return
	}
	s.receiver.mu.Lock()
	defer s.receiver.mu.Unlock()
	s.receiver.observations = append(s.receiver.observations, errorObservation{s.operation, err})
}

// Error returns text frozen when Run stopped waiting.
func (e *diagnosticError) Error() string {
	return e.text
}

// Unwrap preserves typed inspection of the received original error tree.
func (e *diagnosticError) Unwrap() error {
	return e.cause
}

// failure snapshots only completed observations. Late activity completion after
// caller cancellation cannot change the returned text or its original causes.
func (d *runDiagnostics) failure(terminal error) error {
	d.mu.Lock()
	observations := append([]errorObservation(nil), d.observations...)
	d.mu.Unlock()
	causes := make([]error, 0, 1+len(observations))
	causes = append(causes, renderDiagnostic("", terminal))
	for _, observation := range observations {
		causes = append(causes, renderDiagnostic(observation.operation, observation.err))
	}
	return errors.Join(causes...)
}

// renderDiagnostic keeps the original error for errors.Is/As and independently
// renders all single and joined causes, including wrappers with short summaries.
func renderDiagnostic(operation string, err error) error {
	var text strings.Builder
	if operation != "" {
		fmt.Fprintf(&text, "observed %s: ", operation)
	}
	writeErrorTree(&text, err)
	return &diagnosticError{text: text.String(), cause: err}
}

// writeErrorTree visits each exact node once per branch. Matching descendants
// with errors.As here would incorrectly repeat their metadata at every parent.
func writeErrorTree(text *strings.Builder, err error) {
	text.WriteString(errorevidence.Text(err))
	if rejected, ok := err.(*model.OutputValidationError); ok { //nolint:errorlint // Metadata belongs to this exact node, not a descendant.
		fmt.Fprintf(text, " [validation_kind=%s response_evidence=%+v", rejected.Kind(), rejected.Evidence())
		if usage := rejected.Usage(); usage != nil {
			fmt.Fprintf(text, " usage=%+v", *usage)
		} else {
			text.WriteString(" usage=unknown")
		}
		response, copyErr := rejected.RejectedResponse()
		switch {
		case copyErr != nil:
			text.WriteString(" response_copy_error=")
			writeErrorTree(text, copyErr)
		case response == nil:
			text.WriteString(" retained_response=false stop_reason=unknown output_limited=unknown")
		default:
			text.WriteString(" retained_response=true stop_reason=")
			if response.StopReason == "" {
				text.WriteString("unknown")
			} else {
				fmt.Fprintf(text, "%q", response.StopReason)
			}
			fmt.Fprintf(text, " output_limited=%t", response.OutputLimited)
		}
		text.WriteByte(']')
	}
	switch wrapped := err.(type) { //nolint:errorlint // Walk this node's immediate causes without searching descendants.
	case interface{ Unwrap() []error }:
		for _, cause := range wrapped.Unwrap() {
			if cause != nil {
				text.WriteString("\ncaused by: ")
				writeErrorTree(text, cause)
			}
		}
	case interface{ Unwrap() error }:
		if cause := wrapped.Unwrap(); cause != nil {
			text.WriteString("\ncaused by: ")
			writeErrorTree(text, cause)
		}
	}
}
