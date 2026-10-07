// Package mcp writes request-scoped progress before a unary generated result.
// Generated handlers retain responsibility for result encoding and validation;
// this transport owns event framing, the client token, and stream termination.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	progressHTTPResponse struct {
		writer  http.ResponseWriter
		header  http.Header
		body    bytes.Buffer
		status  int
		token   json.RawMessage
		mu      sync.Mutex
		started bool
		closed  bool
		seen    bool
		last    float64
		failure error
	}
)

// ServeProgress lets a generated unary handler report request-scoped progress.
// params must already have passed ValidateHTTPRequest. Without a token or an
// accepted event-stream response, next writes its normal JSON response. With
// updates, this function frames the generated final response as the last event.
// Task operations always use the normal response, even if a client sends a token.
// It returns transport errors instead of dropping notifications or final data.
func ServeProgress(w http.ResponseWriter, r *http.Request, params json.RawMessage, next http.HandlerFunc) (err error) {
	if isTaskOperation(r.Header.Get("Mcp-Method")) {
		next(w, r)
		return nil
	}
	var fields struct {
		Meta map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP wire name.
	}
	if err := json.Unmarshal(params, &fields); err != nil {
		return err
	}
	token, present := fields.Meta["progressToken"]
	if !present || !acceptsEventStream(r.Header.Values("Accept")) {
		next(w, r)
		return nil
	}
	ctx, span := otel.Tracer("goa-ai/mcp").Start(r.Context(), "mcp.progress.response")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	response := &progressHTTPResponse{writer: w, header: w.Header().Clone(), token: cloneRaw(token)}
	defer response.close()
	ctx = context.WithValue(ctx, progressSenderKey{}, progressSender(response.send))
	next(response, r.WithContext(ctx))
	return response.finish()
}

func (r *progressHTTPResponse) Header() http.Header {
	return r.header
}

func (r *progressHTTPResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *progressHTTPResponse) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}

// acceptsEventStream honors the client's media ranges and excludes an explicit
// zero quality for event streams. JSON-only requests still receive JSON.
func acceptsEventStream(headers []string) bool {
	selectedSpecificity := -1
	selectedQuality := float64(0)
	for _, header := range headers {
		for _, entry := range strings.Split(header, ",") {
			media, parameters, err := mime.ParseMediaType(entry)
			if err != nil {
				continue
			}
			specificity := -1
			switch media {
			case "text/event-stream":
				specificity = 2
			case "text/*":
				specificity = 1
			case "*/*":
				specificity = 0
			}
			if specificity < 0 {
				continue
			}
			quality := float64(1)
			if raw, present := parameters["q"]; present {
				quality, err = strconv.ParseFloat(raw, 64)
				if err != nil || math.IsNaN(quality) || math.IsInf(quality, 0) || quality < 0 || quality > 1 {
					continue
				}
			}
			if specificity > selectedSpecificity || specificity == selectedSpecificity && quality > selectedQuality {
				selectedSpecificity, selectedQuality = specificity, quality
			}
		}
	}
	return selectedQuality > 0
}

// writeSSEMessage writes an encoded JSON message as event data and flushes it.
// Progress and subscription responses share this framing and delivery contract.
func writeSSEMessage(writer http.ResponseWriter, data []byte) error {
	var event bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		event.WriteString("data: ")
		event.Write(line)
		event.WriteByte('\n')
	}
	event.WriteByte('\n')
	if _, err := writer.Write(event.Bytes()); err != nil {
		return err
	}
	return http.NewResponseController(writer).Flush()
}

// send serializes concurrent service reports and checks increasing work before
// sending a notification. Final response delivery closes this request's sender.
func (r *progressHTTPResponse) send(ctx context.Context, value float64, total *float64, message *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure != nil {
		return r.failure
	}
	if r.closed {
		return errors.New("MCP progress request has finished")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateProgressValues(value, total); err != nil {
		return err
	}
	if r.seen && value <= r.last {
		return errors.New("progress value must increase within one request")
	}
	data, err := json.Marshal(rpcNotification{JSONRPC: rpcVersion, Method: "notifications/progress", Params: progressWire{Token: r.token, Value: &value, Total: total, Message: message}})
	if err != nil {
		return err
	}
	if !r.started {
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
	r.seen, r.last = true, value
	trace.SpanFromContext(ctx).AddEvent("mcp.progress.sent", trace.WithAttributes(attribute.Float64("progress", value)))
	return nil
}

// finish writes the generated response once and prevents later service reports.
// A handler that produced no progress keeps its original HTTP status and body.
func (r *progressHTTPResponse) finish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.failure != nil {
		return r.failure
	}
	if !r.started {
		for name, values := range r.header {
			r.writer.Header()[name] = values
		}
		if r.status == 0 {
			r.status = http.StatusAccepted
		}
		r.writer.WriteHeader(r.status)
		_, err := r.writer.Write(r.body.Bytes())
		return err
	}
	var response rpcMessage
	if err := json.Unmarshal(r.body.Bytes(), &response); err != nil {
		return fmt.Errorf("generated MCP handler did not write a final JSON-RPC response: %w", err)
	}
	if err := response.validateResponse(); err != nil {
		return err
	}
	return writeSSEMessage(r.writer, r.body.Bytes())
}

// close stops retained service contexts from reporting after the handler returns
// or panics. It does not write a response or hide the handler failure.
func (r *progressHTTPResponse) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}
