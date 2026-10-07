// Package mcp receives changes on one subscriptions/listen request. The peer
// acknowledges the supported filter before sending changes; each message names
// that request, including full state for selected Tasks. The host handles events
// synchronously and owns any later listen.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type (
	// SubscriptionFilter selects the changes requested on one listen operation.
	// False fields and empty selection lists request no notifications of that kind.
	SubscriptionFilter struct {
		// ToolsListChanged requests notification when the tool catalog changes.
		ToolsListChanged bool `json:"toolsListChanged,omitempty"` //nolint:tagliatelle // MCP wire name.
		// PromptsListChanged requests notification when the prompt catalog changes.
		PromptsListChanged bool `json:"promptsListChanged,omitempty"` //nolint:tagliatelle // MCP wire name.
		// ResourcesListChanged requests notification when the resource catalog changes.
		ResourcesListChanged bool `json:"resourcesListChanged,omitempty"` //nolint:tagliatelle // MCP wire name.
		// TaskIDs selects server-owned tasks whose complete state should be reported.
		TaskIDs []string `json:"taskIds,omitempty"` //nolint:tagliatelle // Tasks extension wire name.
		// ResourceSubscriptions identifies the resources whose changes are requested.
		ResourceSubscriptions []string `json:"resourceSubscriptions,omitempty"` //nolint:tagliatelle // MCP wire name.
	}

	// SubscriptionEvent is a validated acknowledgment or change from one listen.
	SubscriptionEvent struct {
		// RequestID is the JSON-encoded ID assigned to the listen request.
		RequestID json.RawMessage
		// Kind identifies the acknowledgment or the resource, catalog or Task change.
		Kind SubscriptionEventKind
		// Accepted contains the peer's filter only for SubscriptionAcknowledged.
		Accepted SubscriptionFilter
		// URI is the changed resource address only for SubscriptionResourceUpdated.
		// The service may report a sub-resource of an accepted resource address.
		URI string
		// Task contains the full task observation only for SubscriptionTaskChanged.
		Task *Task
		// Meta preserves the notification's namespaced metadata as a JSON object.
		Meta json.RawMessage
	}

	// SubscriptionEventKind identifies one core or Tasks extension notification.
	SubscriptionEventKind string

	// SubscriptionCancelledError reports a server-initiated cancellation on stdio.
	// It differs from host cancellation and from an unexpected transport loss.
	SubscriptionCancelledError struct {
		// RequestID identifies the listen request cancelled by the server.
		RequestID json.RawMessage
		// Reason is the optional explanation, including an explicit empty string.
		Reason *string
	}

	subscriptionHandlerKey struct{}
	subscriptionHandler    func(context.Context, SubscriptionEvent) error
	subscriptionReceiver   struct {
		requestID json.RawMessage
		key       string
		subscriptionState
		handler      subscriptionHandler
		inputSupport InputSupport
	}
)

const (
	// SubscriptionAcknowledged reports which requested notifications are supported.
	SubscriptionAcknowledged SubscriptionEventKind = "notifications/subscriptions/acknowledged"
	// SubscriptionToolsChanged reports that the client should reload the tool catalog.
	SubscriptionToolsChanged SubscriptionEventKind = "notifications/tools/list_changed"
	// SubscriptionPromptsChanged reports that the client should reload the prompt catalog.
	SubscriptionPromptsChanged SubscriptionEventKind = "notifications/prompts/list_changed"
	// SubscriptionResourcesChanged reports that the client should reload the resource catalog.
	SubscriptionResourcesChanged SubscriptionEventKind = "notifications/resources/list_changed"
	// SubscriptionResourceUpdated reports that a resource should be read again.
	SubscriptionResourceUpdated SubscriptionEventKind = "notifications/resources/updated"
	// SubscriptionTaskChanged carries the complete state of an accepted task ID.
	SubscriptionTaskChanged SubscriptionEventKind = "notifications/tasks"

	methodSubscriptionsListen = "subscriptions/listen"
	methodCancelled           = "notifications/cancelled"
	subscriptionIDKey         = "io.modelcontextprotocol/subscriptionId"
)

// WithSubscriptionEvents delivers validated notifications from each generated
// subscriptions/listen call using the returned context. The generated payload
// selects the filter; the handler receives its acknowledgment and later changes.
// Handlers run synchronously, and an error ends that request without reconnecting.
// Include WithTaskSupport when a generated listen payload selects task IDs.
// A nil handler is a construction error and panics.
func WithSubscriptionEvents(ctx context.Context, handler func(context.Context, SubscriptionEvent) error) context.Context {
	if handler == nil {
		panic("mcp: subscription handler is required")
	}
	return context.WithValue(ctx, subscriptionHandlerKey{}, subscriptionHandler(handler))
}

// Listen receives requested changes over HTTP until graceful completion, host
// cancellation, callback failure, or transport loss. It sends one POST and does
// not reconnect. The handler receives the acknowledgment first. Selecting task
// IDs advertises Tasks support for this request and delivers their full state.
func (c *HTTPCaller) Listen(ctx context.Context, filter SubscriptionFilter, handler func(context.Context, SubscriptionEvent) error) error {
	return c.transport.Listen(ctx, c.endpoint, filter, handler)
}

// Listen receives requested changes over stdio on a distinct request. Cancelling
// ctx sends notifications/cancelled for that request and releases a blocked reader.
// Concurrent calls and listeners retain their own IDs, filters, and callbacks.
// Selecting task IDs advertises Tasks support and delivers their full state.
func (c *StdioCaller) Listen(ctx context.Context, filter SubscriptionFilter, handler func(context.Context, SubscriptionEvent) error) error {
	if handler == nil {
		return errors.New("mcp: subscription handler is required")
	}
	ctx = WithSubscriptionEvents(ctx, handler)
	if len(filter.TaskIDs) > 0 {
		ctx = WithTaskSupport(ctx)
	}
	var result json.RawMessage
	return c.call(ctx, methodSubscriptionsListen, map[string]any{"notifications": cloneSubscriptionFilter(filter)}, &result)
}

// Listen receives changes from an already-built HTTP transport. Generated
// callers can supply their endpoint without constructing a second transport.
func (t *HTTPTransport) Listen(ctx context.Context, endpoint string, filter SubscriptionFilter, handler func(context.Context, SubscriptionEvent) error) error {
	if handler == nil {
		return errors.New("mcp: subscription handler is required")
	}
	ctx = WithSubscriptionEvents(ctx, handler)
	if len(filter.TaskIDs) > 0 {
		ctx = WithTaskSupport(ctx)
	}
	var result json.RawMessage
	return t.call(ctx, endpoint, methodSubscriptionsListen, map[string]any{"notifications": cloneSubscriptionFilter(filter)}, &result)
}

// Error reports the peer's optional cancellation explanation.
func (e *SubscriptionCancelledError) Error() string {
	if e.Reason == nil {
		return "MCP subscription cancelled by server"
	}
	return "MCP subscription cancelled by server: " + *e.Reason
}

// newSubscriptionReceiver decodes the outgoing filter once. State belongs to
// this request, so one acknowledgment cannot authorize another listener's events.
func newSubscriptionReceiver(ctx context.Context, id, filter json.RawMessage, support InputSupport) (*subscriptionReceiver, error) {
	key, err := protocolIDKey(id)
	if err != nil {
		return nil, err
	}
	requested, err := decodeSubscriptionFilter(filter)
	if err != nil {
		return nil, err
	}
	if len(requested.TaskIDs) > 0 && ctx.Value(taskSupportKey{}) == nil {
		return nil, errors.New("task subscriptions require declared Tasks support")
	}
	handler, ok := ctx.Value(subscriptionHandlerKey{}).(subscriptionHandler)
	if !ok {
		return nil, errors.New("mcp: subscription handler is required")
	}
	return &subscriptionReceiver{
		requestID:         cloneRaw(id),
		key:               key,
		subscriptionState: subscriptionState{requested: requested},
		handler:           handler,
		inputSupport:      support,
	}, nil
}

// decodeSubscriptionFilter rejects unknown fields, null and invalid values
// before they can be discarded or become an unrequested notification kind.
func decodeSubscriptionFilter(raw json.RawMessage) (SubscriptionFilter, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return SubscriptionFilter{}, errors.New("subscription notifications must be an object")
	}
	for _, name := range []string{"toolsListChanged", "promptsListChanged", "resourcesListChanged", "resourceSubscriptions", "taskIds"} {
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return SubscriptionFilter{}, fmt.Errorf("subscription %s cannot be null", name)
		}
	}
	for _, name := range []string{"resourceSubscriptions", "taskIds"} {
		if values, present := fields[name]; present {
			var selections []json.RawMessage
			if err := json.Unmarshal(values, &selections); err != nil {
				return SubscriptionFilter{}, err
			}
			for _, selection := range selections {
				if bytes.Equal(bytes.TrimSpace(selection), []byte("null")) {
					return SubscriptionFilter{}, fmt.Errorf("subscription %s strings cannot be null", name)
				}
			}
		}
	}
	var filter SubscriptionFilter
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&filter); err != nil {
		return SubscriptionFilter{}, err
	}
	return filter, nil
}

// cloneSubscriptionFilter prevents host callbacks from changing the filter used
// to authorize later events on the same request.
func cloneSubscriptionFilter(filter SubscriptionFilter) SubscriptionFilter {
	filter.ResourceSubscriptions = slices.Clone(filter.ResourceSubscriptions)
	filter.TaskIDs = slices.Clone(filter.TaskIDs)
	return filter
}

// subscriptionMessageID extracts the listen ID from a notification. Stdio
// cancellation names it in requestId; all change messages carry it in _meta.
func subscriptionMessageID(message rpcMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(message.Params, &fields); err != nil || fields == nil {
		return nil, errors.New("subscription notification params must be an object")
	}
	if message.Method == methodCancelled {
		if _, err := protocolIDKey(fields["requestId"]); err != nil {
			return nil, err
		}
		return fields["requestId"], nil
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(fields["_meta"], &meta); err != nil || meta == nil {
		return nil, errors.New("subscription notification metadata is required")
	}
	if _, err := protocolIDKey(meta[subscriptionIDKey]); err != nil {
		return nil, fmt.Errorf("subscription ID: %w", err)
	}
	return meta[subscriptionIDKey], nil
}

// decodeSubscriptionCancellation checks a stdio control notification before it
// can terminate a listener. Invalid control messages must not cancel other work.
func decodeSubscriptionCancellation(raw json.RawMessage) (*SubscriptionCancelledError, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("cancellation params must be an object")
	}
	if _, err := protocolIDKey(fields["requestId"]); err != nil {
		return nil, err
	}
	if err := validateMeta(fields["_meta"]); err != nil {
		return nil, err
	}
	var reason *string
	if value, present := fields["reason"]; present {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("cancellation reason cannot be null")
		}
		if err := json.Unmarshal(value, &reason); err != nil {
			return nil, err
		}
	}
	return &SubscriptionCancelledError{RequestID: cloneRaw(fields["requestId"]), Reason: reason}, nil
}

// isSubscriptionNotification selects listen notifications routed by stdio.
// Task state uses the same request correlation as resource and catalog changes.
func isSubscriptionNotification(method string) bool {
	switch SubscriptionEventKind(method) {
	case SubscriptionAcknowledged, SubscriptionToolsChanged, SubscriptionPromptsChanged, SubscriptionResourcesChanged, SubscriptionResourceUpdated, SubscriptionTaskChanged:
		return true
	default:
		return method == methodCancelled
	}
}

// notification verifies identity and ordering before the callback receives a
// typed event. Callback errors end only this listener and never trigger a retry.
func (r *subscriptionReceiver) notification(ctx context.Context, message rpcMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := subscriptionMessageID(message)
	if err != nil {
		return NewMalformedResponseError(err)
	}
	key, err := protocolIDKey(id)
	if err != nil || key != r.key {
		return NewMalformedResponseError(errors.New("subscription ID does not match the listen request"))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(message.Params, &fields); err != nil {
		return NewMalformedResponseError(err)
	}
	if message.Method == methodCancelled {
		cancelled, err := decodeSubscriptionCancellation(message.Params)
		if err != nil {
			return NewMalformedResponseError(err)
		}
		cancelled.RequestID = cloneRaw(r.requestID)
		return cancelled
	}
	event := SubscriptionEvent{RequestID: cloneRaw(r.requestID), Kind: SubscriptionEventKind(message.Method), Meta: cloneRaw(fields["_meta"])}
	if event.Kind == SubscriptionAcknowledged {
		accepted, err := decodeSubscriptionFilter(fields["notifications"])
		if err != nil {
			return NewMalformedResponseError(err)
		}
		if err := r.acknowledge(accepted); err != nil {
			return NewMalformedResponseError(err)
		}
		event.Accepted = accepted
	} else {
		subject := ""
		switch event.Kind {
		case SubscriptionAcknowledged, SubscriptionToolsChanged, SubscriptionPromptsChanged, SubscriptionResourcesChanged:
		case SubscriptionResourceUpdated:
			if bytes.Equal(bytes.TrimSpace(fields["uri"]), []byte("null")) || json.Unmarshal(fields["uri"], &event.URI) != nil {
				return NewMalformedResponseError(errors.New("updated resource URI is required"))
			}
			subject = event.URI
		case SubscriptionTaskChanged:
			task, err := decodeDetailedTask(message.Params)
			if err != nil {
				return NewMalformedResponseError(err)
			}
			subject = task.info.TaskID
			task, err = normalizeTask(ctx, subject, task, r.inputSupport)
			if err != nil {
				return err
			}
			event.Task = &task
		}
		if err := r.change(event.Kind, subject); err != nil {
			return NewMalformedResponseError(err)
		}
	}
	trace.SpanFromContext(ctx).AddEvent("mcp.subscription.notification", trace.WithAttributes(attribute.String("notification.method", message.Method)))
	if err := r.handler(ctx, event); err != nil {
		return fmt.Errorf("deliver MCP subscription notification: %w", err)
	}
	return nil
}

// finish accepts only a completed result for this acknowledged subscription.
// A transport close without this response remains an interruption error.
func (r *subscriptionReceiver) finish(raw json.RawMessage) error {
	var result struct {
		ResultType string                     `json:"resultType"` //nolint:tagliatelle // MCP wire name.
		Meta       map[string]json.RawMessage `json:"_meta"`      //nolint:tagliatelle // MCP wire name.
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return NewMalformedResponseError(err)
	}
	key, err := protocolIDKey(result.Meta[subscriptionIDKey])
	if err != nil || key != r.key || result.ResultType != resultComplete || !r.acknowledged {
		return NewMalformedResponseError(errors.New("invalid subscription completion or missing acknowledgment"))
	}
	return nil
}
