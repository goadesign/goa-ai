// Package mcp keeps HTTP requests active while unary service methods work.
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
	"time"

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

		writeMu      sync.Mutex
		writing      bool
		terminated   bool
		interruptErr error
	}
)

// ServeProgress keeps an eligible MCP response open while its handler works.
// Fast handlers retain their ordinary JSON response. Slower handlers send SSE
// comments every 15 seconds, followed by the generated final response. Comments
// carry no progress; service updates still require the client's progress token.
// JSON-only requests and Task operations retain their ordinary responses.
// Call this after authentication, protocol validation and HTTP middleware have
// accepted the request and chosen its response headers.
func ServeProgress(w http.ResponseWriter, r *http.Request, params json.RawMessage, next http.HandlerFunc) error {
	return serveProgress(w, r, params, next, 15*time.Second)
}

// serveProgress owns one response's timer and joins it before returning. The
// interval controls connection activity, never the duration of service work.
func serveProgress(w http.ResponseWriter, r *http.Request, params json.RawMessage, next http.HandlerFunc, interval time.Duration) (err error) {
	if isTaskOperation(r.Header.Get("Mcp-Method")) || !acceptsEventStream(r.Header.Values("Accept")) {
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
	ctx, span := otel.Tracer("goa-ai/mcp").Start(r.Context(), "mcp.progress.response")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	response := &progressHTTPResponse{writer: w, header: w.Header().Clone(), token: cloneRaw(token)}
	stopped, done := make(chan struct{}), make(chan struct{})
	interrupted := make(chan struct{})
	stopInterrupt := context.AfterFunc(ctx, func() {
		response.interrupt()
		close(interrupted)
	})
	completed := false
	joined := false
	defer func() {
		// A panic must release a blocked comment write before joining its worker.
		// Ordinary completion retains the HTTP server's existing write deadline.
		if !completed {
			response.interrupt()
		}
		if !joined {
			close(stopped)
			<-done
		}
		if !stopInterrupt() {
			<-interrupted
		}
		response.close()
		err = errors.Join(err, response.interruptErr)
	}()
	go response.keepalive(ctx, interval, stopped, done, cancel)
	if present {
		ctx = context.WithValue(ctx, progressSenderKey{}, progressSender(response.send))
	}
	next(response, r.WithContext(ctx))
	close(stopped)
	<-done
	joined = true
	err = response.finish()
	completed = true
	return err
}

// keepalive sends transport comments while service work is silent. A failed
// write cancels only this HTTP request context so its handler can stop waiting.
func (r *progressHTTPResponse) keepalive(ctx context.Context, interval time.Duration, stopped <-chan struct{}, done chan<- struct{}, cancel context.CancelCauseFunc) {
	defer close(done)
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-stopped:
			return
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := r.comment(ctx); err != nil {
				cancel(err)
				return
			}
		}
	}
}

// comment shares the progress/final write lock so bytes from separate messages
// cannot interleave. Colon lines are ignored by SSE clients, not tool updates.
func (r *progressHTTPResponse) comment(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure != nil {
		return r.failure
	}
	if r.closed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.beginWrite(); err != nil {
		return err
	}
	defer r.endWrite()
	r.startStream()
	if _, err := r.writer.Write([]byte(":\n\n")); err != nil {
		r.failure = err
		return err
	}
	r.failure = http.NewResponseController(r.writer).Flush()
	if r.failure == nil {
		trace.SpanFromContext(ctx).AddEvent("mcp.keepalive.sent")
	}
	return r.failure
}

// beginWrite marks the network write before cancellation can interrupt it.
// The separate lock lets cancellation release a writer holding the message lock.
func (r *progressHTTPResponse) beginWrite() error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if r.terminated {
		return context.Canceled
	}
	r.writing = true
	return nil
}

func (r *progressHTTPResponse) endWrite() {
	r.writeMu.Lock()
	r.writing = false
	r.writeMu.Unlock()
}

// interrupt expires only an active terminal write. It never changes a healthy
// response's deadline or restores a deadline chosen by the HTTP server. Custom
// writers must support write deadlines to interrupt a blocked network write.
func (r *progressHTTPResponse) interrupt() {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if r.terminated {
		return
	}
	r.terminated = true
	if r.writing {
		r.interruptErr = http.NewResponseController(r.writer).SetWriteDeadline(time.Now())
	}
}

// startStream changes framing only when the first comment or progress update
// is written. The generated handler still owns the final JSON-RPC response.
func (r *progressHTTPResponse) startStream() {
	if r.started {
		return
	}
	r.writer.Header().Del("Content-Length")
	r.writer.Header().Set("Content-Type", "text/event-stream")
	r.writer.Header().Set("Cache-Control", "no-store")
	r.writer.Header().Set("X-Accel-Buffering", "no")
	r.writer.WriteHeader(http.StatusOK)
	r.started = true
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
	if err := r.beginWrite(); err != nil {
		return err
	}
	defer r.endWrite()
	r.startStream()
	if err := writeSSEMessage(r.writer, data); err != nil {
		r.failure = err
		return err
	}
	r.seen, r.last = true, value
	trace.SpanFromContext(ctx).AddEvent("mcp.progress.sent", trace.WithAttributes(attribute.Float64("progress", value)))
	return nil
}

// finish writes the generated response once and prevents later service reports.
// A handler that finishes before streaming starts keeps its HTTP status and body.
func (r *progressHTTPResponse) finish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.failure != nil {
		return r.failure
	}
	if err := r.beginWrite(); err != nil {
		return err
	}
	defer r.endWrite()
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
