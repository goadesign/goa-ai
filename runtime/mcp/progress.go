// Package mcp correlates progress with one active protocol request. Clients
// choose tokens and deliver ordered updates; services report values through
// their request context without selecting transport identifiers.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type (
	// Progress describes one server update for an active request. Values restart
	// on a new request, including a retry or a host-input continuation round.
	Progress struct {
		// RequestID is the exact JSON-RPC request ID encoded as JSON. The client
		// assigns it; hosts use it to distinguish concurrent requests and retries.
		RequestID json.RawMessage
		// Value is the service's increasing amount of completed work.
		Value float64
		// Total is the amount of work when the service knows it.
		Total *float64
		// Message is the service's explanation, including an explicit empty string.
		Message *string
	}

	progressHandlerKey struct{}
	progressSenderKey  struct{}
	progressHandler    func(context.Context, Progress) error
	progressSender     func(context.Context, float64, *float64, *string) error

	progressWire struct {
		Token   json.RawMessage `json:"progressToken"` //nolint:tagliatelle // MCP wire name.
		Value   *float64        `json:"progress"`
		Total   *float64        `json:"total,omitempty"`
		Message *string         `json:"message,omitempty"`
		Meta    json.RawMessage `json:"_meta,omitempty"` //nolint:tagliatelle // MCP wire name.
	}

	progressReceiver struct {
		token     string
		requestID json.RawMessage
		handler   progressHandler
		seen      bool
		last      float64
	}
)

// WithProgress requests updates for eligible MCP operations using the returned
// context. The handler runs in that operation's goroutine before its final
// result. Handler errors stop the request; tool outcomes can then be unknown.
// Task get, update and cancel operations do not request progress.
// A nil handler is a construction error and panics.
func WithProgress(ctx context.Context, handler func(context.Context, Progress) error) context.Context {
	if handler == nil {
		panic("mcp: progress handler is required")
	}
	return context.WithValue(ctx, progressHandlerKey{}, progressHandler(handler))
}

// ReportProgress sends increasing work values through an active MCP request.
// The transport supplies its token and owns notification delivery. When the
// caller did not request updates, including an ordinary HTTP service call,
// reporting is a no-op. A closed stream, invalid value, or delivery failure
// returns an error that the service must handle.
func ReportProgress(ctx context.Context, value float64, total *float64, message *string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sender, ok := ctx.Value(progressSenderKey{}).(progressSender)
	if !ok {
		return nil
	}
	return sender(ctx, value, total, message)
}

// newProgressReceiver gives one request its own token and increasing-value
// checks. Task operations ignore an inherited callback and reject explicit tokens;
// other methods check manually supplied tokens even without a host handler.
func newProgressReceiver(ctx context.Context, method string, id json.RawMessage, meta map[string]json.RawMessage) (*progressReceiver, error) {
	if isTaskOperation(method) {
		if _, present := meta["progressToken"]; present {
			return nil, fmt.Errorf("%s does not support progress", method)
		}
		return nil, nil
	}
	handler, _ := ctx.Value(progressHandlerKey{}).(progressHandler)
	if handler != nil {
		encoded, err := json.Marshal(uuid.NewString())
		if err != nil {
			return nil, err
		}
		meta["progressToken"] = encoded
	}
	token, present := meta["progressToken"]
	if !present {
		return nil, nil
	}
	key, err := protocolIDKey(token)
	if err != nil {
		return nil, err
	}
	return &progressReceiver{token: key, requestID: cloneRaw(id), handler: handler}, nil
}

// decodeProgress checks required values before optional null can become absence.
func decodeProgress(raw json.RawMessage) (*progressWire, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("progress params must be an object")
	}
	for _, field := range []string{"progressToken", "progress", "total", "message"} {
		if bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) {
			return nil, fmt.Errorf("progress %s cannot be null", field)
		}
	}
	var update progressWire
	if err := json.Unmarshal(raw, &update); err != nil {
		return nil, err
	}
	if _, err := protocolIDKey(update.Token); err != nil {
		return nil, err
	}
	if update.Value == nil {
		return nil, errors.New("progress value is required")
	}
	if err := validateProgressValues(*update.Value, update.Total); err != nil {
		return nil, err
	}
	if err := validateMeta(update.Meta); err != nil {
		return nil, err
	}
	return &update, nil
}

// protocolIDKey compares integer request IDs and progress tokens by value.
// Strings keep their exact characters; callers retain the original wire bytes.
func protocolIDKey(raw json.RawMessage) (string, error) {
	if !json.Valid(raw) {
		return "", errors.New("protocol identifier must be a string or integer")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "string:" + text, nil
	}
	number, ok := new(big.Rat).SetString(string(bytes.TrimSpace(raw)))
	if !ok || !number.IsInt() {
		return "", errors.New("protocol identifier must be a string or integer")
	}
	return "integer:" + number.Num().String(), nil
}

// validateProgressValues rejects values JSON cannot represent. The protocol
// defines no percentage scale, non-negative bound, or relationship to total.
func validateProgressValues(value float64, total *float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || total != nil && (math.IsNaN(*total) || math.IsInf(*total, 0)) {
		return errors.New("progress and total must be finite numbers")
	}
	return nil
}

// accept checks one request's token and ordering before the host observes it.
func (r *progressReceiver) accept(ctx context.Context, update *progressWire) error {
	key, err := protocolIDKey(update.Token)
	if err != nil {
		return err
	}
	if r == nil || key != r.token {
		return NewMalformedResponseError(errors.New("progress token does not match an active request"))
	}
	if r.seen && *update.Value <= r.last {
		return NewMalformedResponseError(errors.New("progress value must increase within one request"))
	}
	r.seen, r.last = true, *update.Value
	trace.SpanFromContext(ctx).AddEvent("mcp.progress", trace.WithAttributes(attribute.Float64("progress", *update.Value)))
	if r.handler == nil {
		return nil
	}
	if err := r.handler(ctx, Progress{RequestID: cloneRaw(r.requestID), Value: *update.Value, Total: update.Total, Message: update.Message}); err != nil {
		return fmt.Errorf("deliver MCP progress: %w", err)
	}
	return nil
}

// notification accepts progress for this HTTP request and leaves unrelated
// extension notifications outside the progress contract.
func (r *progressReceiver) notification(ctx context.Context, message rpcMessage) error {
	if message.Method != "notifications/progress" {
		return nil
	}
	update, err := decodeProgress(message.Params)
	if err != nil {
		return NewMalformedResponseError(err)
	}
	if err := r.accept(ctx, update); err != nil {
		return err
	}
	return nil
}
