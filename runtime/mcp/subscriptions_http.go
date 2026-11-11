// Package mcp sends changes on one HTTP subscriptions/listen response. The
// configured source chooses the authorized filter and reports changes; this
// transport checks their order, attaches the request ID, and writes completion.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	subscriptionSenderKey struct{}

	// subscriptionSender delivers acknowledged subscription events for one active
	// subscriptions/listen request on either HTTP or stdio.
	subscriptionSender interface {
		send(ctx context.Context, kind SubscriptionEventKind, accepted *SubscriptionFilter, uri string, task map[string]json.RawMessage) error
	}

	// subscriptionHTTPResponse owns output and accepted filters for one request.
	// The generated handler's final response stays private until it is complete.
	subscriptionHTTPResponse struct {
		writer  http.ResponseWriter
		header  http.Header
		body    bytes.Buffer
		status  int
		id      json.RawMessage
		key     string
		state   subscriptionState
		mu      sync.Mutex
		started bool
		closed  bool
		failure error
	}

	subscriptionNotificationParams struct {
		Meta          map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP wire name.
		Notifications *SubscriptionFilter        `json:"notifications,omitempty"`
		URI           string                     `json:"uri,omitempty"`
	}
)

// ServeSubscriptions sends source notifications before a generated handler's
// final response. Call it only for a validated subscriptions/listen HTTP request.
// The handler must acknowledge the authorized subset before reporting changes.
// Returning a complete result ends the stream; cancellation and failed writes
// return errors. This function neither authenticates nor advertises a source.
func ServeSubscriptions(w http.ResponseWriter, r *http.Request, id, params json.RawMessage, next http.HandlerFunc) (err error) {
	key, filter, err := parseSubscriptionRequest(id, params)
	if err != nil {
		return err
	}
	if !acceptsEventStream(r.Header.Values("Accept")) {
		return errors.New("MCP subscription requires an accepted event-stream response")
	}
	ctx, span := otel.Tracer("goa-ai/mcp").Start(r.Context(), "mcp.subscription.response")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	response := &subscriptionHTTPResponse{
		writer: w, header: w.Header().Clone(), id: cloneRaw(id), key: key,
		state: subscriptionState{requested: filter},
	}
	defer response.close()
	ctx = context.WithValue(ctx, subscriptionSenderKey{}, subscriptionSender(response))
	next(response, r.WithContext(ctx))
	return response.finish(ctx)
}

// AcknowledgeSubscription accepts an authorized subset for the current listen
// request and sends its first notification. The transport rejects extra kinds,
// extra resources, and repeated acknowledgment. Callers cannot choose its ID.
func AcknowledgeSubscription(ctx context.Context, accepted SubscriptionFilter) error {
	return sendSubscription(ctx, SubscriptionAcknowledged, &accepted, "", nil)
}

// ReportToolsChanged tells the current listener to reload its tool catalog.
// The source must own a changing catalog; unaccepted notifications are rejected.
func ReportToolsChanged(ctx context.Context) error {
	return sendSubscription(ctx, SubscriptionToolsChanged, nil, "", nil)
}

// ReportPromptsChanged tells the current listener to reload its prompt catalog.
// The source must own a changing catalog; unaccepted notifications are rejected.
func ReportPromptsChanged(ctx context.Context) error {
	return sendSubscription(ctx, SubscriptionPromptsChanged, nil, "", nil)
}

// ReportResourcesChanged tells the current listener to reload its resource catalog.
// The source must own a changing catalog; unaccepted notifications are rejected.
func ReportResourcesChanged(ctx context.Context) error {
	return sendSubscription(ctx, SubscriptionResourcesChanged, nil, "", nil)
}

// ReportResourceUpdated sends a changed resource address to the current listener.
// The address may identify an accepted resource's sub-resource. The source owns
// that relationship and access checks; the transport checks its URI syntax and
// requires acknowledged resource subscriptions.
func ReportResourceUpdated(ctx context.Context, uri string) error {
	return sendSubscription(ctx, SubscriptionResourceUpdated, nil, uri, nil)
}

// ReportTaskChanged sends an encoded tasks/get result on the current listener.
// Generated adapters reuse their read converter and codec to supply this complete
// snapshot. The transport validates the same state as a task read, removes the
// response discriminator, and adds the listen request's correlation metadata.
// The source must authorize and acknowledge the exact task ID before this call.
func ReportTaskChanged(ctx context.Context, snapshot json.RawMessage) error {
	var result taskGetResult
	if err := json.Unmarshal(snapshot, &result); err != nil {
		return fmt.Errorf("invalid task subscription snapshot: %w", err)
	}
	if result.task.input != nil {
		if err := result.task.input.Validate(InputSupport{Form: true, URL: true}); err != nil {
			return err
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &fields); err != nil {
		return err
	}
	if err := validateMeta(fields["_meta"]); err != nil {
		return err
	}
	delete(fields, "resultType")
	return sendSubscription(ctx, SubscriptionTaskChanged, nil, result.task.info.TaskID, fields)
}

func (r *subscriptionHTTPResponse) Header() http.Header {
	return r.header
}

func (r *subscriptionHTTPResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *subscriptionHTTPResponse) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}

// parseSubscriptionRequest validates the request ID and requested notification
// filter for one subscriptions/listen operation.
func parseSubscriptionRequest(id, params json.RawMessage) (string, SubscriptionFilter, error) {
	key, err := protocolIDKey(id)
	if err != nil {
		return "", SubscriptionFilter{}, err
	}
	var input struct {
		Notifications json.RawMessage `json:"notifications"`
	}
	if err := json.Unmarshal(params, &input); err != nil {
		return "", SubscriptionFilter{}, err
	}
	filter, err := decodeSubscriptionFilter(input.Notifications)
	if err != nil {
		return "", SubscriptionFilter{}, err
	}
	return key, filter, nil
}

// sendSubscription obtains the request-owned writer and refuses ordinary service
// contexts. A source cannot accidentally send changes on another operation.
func sendSubscription(ctx context.Context, kind SubscriptionEventKind, accepted *SubscriptionFilter, uri string, task map[string]json.RawMessage) (err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.subscription.notification.send", trace.WithAttributes(attribute.String("notification.method", string(kind))))
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	sender, ok := ctx.Value(subscriptionSenderKey{}).(subscriptionSender)
	if !ok {
		return errors.New("MCP subscription report requires an active listen request")
	}
	return sender.send(ctx, kind, accepted, uri, task)
}

// encodeSubscriptionNotification validates one acknowledgment or change against
// the request's subscription state and encodes its JSON-RPC notification bytes.
func encodeSubscriptionNotification(state *subscriptionState, id json.RawMessage, kind SubscriptionEventKind, accepted *SubscriptionFilter, uri string, task map[string]json.RawMessage) ([]byte, error) {
	params := subscriptionNotificationParams{Meta: map[string]json.RawMessage{subscriptionIDKey: id}, Notifications: accepted, URI: uri}
	if kind == SubscriptionAcknowledged {
		if err := state.acknowledge(*accepted); err != nil {
			return nil, err
		}
	} else if err := state.change(kind, uri); err != nil {
		return nil, err
	}
	var notificationParams any = params
	if kind == SubscriptionTaskChanged {
		meta := make(map[string]json.RawMessage)
		if raw, present := task["_meta"]; present {
			if err := json.Unmarshal(raw, &meta); err != nil {
				return nil, err
			}
		}
		if _, present := meta[subscriptionIDKey]; present {
			return nil, errors.New("task source must leave subscription ID ownership to the transport")
		}
		meta[subscriptionIDKey] = id
		encoded, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		task["_meta"] = encoded
		notificationParams = task
	}
	return json.Marshal(rpcNotification{JSONRPC: rpcVersion, Method: string(kind), Params: notificationParams})
}

// completeSubscriptionResult validates a completed subscription result and
// attaches the transport-owned subscription ID to its _meta object.
func completeSubscriptionResult(state *subscriptionState, id, rawResult json.RawMessage) (json.RawMessage, error) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(rawResult, &result); err != nil || result == nil {
		return nil, errors.New("generated MCP subscription completion must be an object")
	}
	for name := range result {
		if name != "resultType" && name != "_meta" {
			return nil, fmt.Errorf("generated MCP subscription completion contains unexpected field %q", name)
		}
	}
	if !state.acknowledged {
		return nil, &Error{Code: JSONRPCInternalError, Message: "MCP subscription completion requires acknowledgment before a finished result"}
	}
	var resultType string
	if err := json.Unmarshal(result["resultType"], &resultType); err != nil || resultType != resultComplete {
		return nil, errors.New("generated MCP subscription completion requires acknowledgment and resultType complete")
	}
	meta := make(map[string]json.RawMessage)
	if raw, present := result["_meta"]; present {
		if err := json.Unmarshal(raw, &meta); err != nil || meta == nil {
			return nil, errors.New("generated MCP subscription completion metadata must be an object")
		}
	}
	if _, present := meta[subscriptionIDKey]; present {
		return nil, errors.New("generated MCP handler must leave subscription ID ownership to the transport")
	}
	meta[subscriptionIDKey] = id
	var err error
	result["_meta"], err = json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// send checks the request's accepted filter before writing any bytes. Each
// request serializes sends and remembers its first delivery error.
func (r *subscriptionHTTPResponse) send(ctx context.Context, kind SubscriptionEventKind, accepted *SubscriptionFilter, uri string, task map[string]json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writable(ctx); err != nil {
		return err
	}
	data, err := encodeSubscriptionNotification(&r.state, r.id, kind, accepted, uri, task)
	if err != nil {
		return err
	}
	if !r.started {
		for name, values := range r.header {
			r.writer.Header()[name] = values
		}
		r.writer.Header().Del("Content-Length")
		r.writer.Header().Set("Content-Type", "text/event-stream")
		r.writer.Header().Set("Cache-Control", "no-store")
		r.writer.WriteHeader(http.StatusOK)
		r.started = true
	}
	if err := writeSSEMessage(r.writer, data); err != nil {
		r.failure = err
		return err
	}
	trace.SpanFromContext(ctx).AddEvent("mcp.subscription.sent", trace.WithAttributes(attribute.String("notification.method", string(kind))))
	return nil
}

// finish retains generated errors and requires acknowledged, complete success.
// It adds the transport-owned subscription ID without changing other metadata.
func (r *subscriptionHTTPResponse) finish(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writable(ctx); err != nil {
		return err
	}
	r.closed = true
	var response rpcMessage
	if err := json.Unmarshal(r.body.Bytes(), &response); err != nil {
		return fmt.Errorf("generated MCP handler did not write a final JSON-RPC response: %w", err)
	}
	if err := response.validateResponse(); err != nil {
		return err
	}
	key, err := protocolIDKey(response.ID)
	if err != nil || key != r.key {
		return errors.New("generated MCP subscription response ID does not match its request")
	}
	data := r.body.Bytes()
	if len(response.Error) == 0 {
		completed, err := completeSubscriptionResult(&r.state, r.id, response.Result)
		if err != nil {
			return err
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(data, &envelope); err != nil {
			return err
		}
		envelope["result"] = completed
		data, err = json.Marshal(envelope)
		if err != nil {
			return err
		}
	}
	if r.started {
		return writeSSEMessage(r.writer, data)
	}
	for name, values := range r.header {
		r.writer.Header()[name] = values
	}
	r.writer.WriteHeader(r.status)
	_, err = r.writer.Write(data)
	return err
}

// writable returns the first delivery failure, cancellation, or closed lifetime.
func (r *subscriptionHTTPResponse) writable(ctx context.Context) error {
	if r.failure != nil {
		return r.failure
	}
	if r.closed {
		return errors.New("MCP subscription request has finished")
	}
	return ctx.Err()
}

// close prevents retained source contexts from sending after return or panic.
func (r *subscriptionHTTPResponse) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}
