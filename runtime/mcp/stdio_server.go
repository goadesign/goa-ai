// Package mcp serves MCP requests and notifications over newline-delimited
// standard input and output streams using the same protocol validation,
// progress reporting, and subscription state as the HTTP transport.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goa.design/goa/v3/jsonrpc"
)

type (
	// StdioHandlerFunc dispatches one validated MCP JSON-RPC request on a stdio
	// connection and returns its JSON-RPC response.
	StdioHandlerFunc func(ctx context.Context, req *jsonrpc.RawRequest, legacy bool) *jsonrpc.Response

	// stdioConnKey associates an active stdio connection with a request context
	// so subscription handlers can emit notifications on the same stream.
	stdioConnKey struct{}

	// stdioStream owns a raw *os.File and its runtime-poller-registered
	// counterpart so connection teardown can interrupt in-flight reads and writes.
	stdioStream struct {
		// raw is the caller-supplied descriptor closed when ServeStdio returns.
		raw *os.File
		// pollable is the runtime-poller-backed file used for all reads or writes.
		pollable *os.File
		// interruptOnce guards deadline expiration and closing of pollable.
		interruptOnce sync.Once
		// closeOnce guards closing of raw when raw differs from pollable.
		closeOnce sync.Once
		// closeErr retains the first non-closed error from interrupting or closing.
		closeErr error
	}

	// stdioReadResult carries one newline-delimited message or terminal read error
	// from the reader goroutine to the connection loop.
	stdioReadResult struct {
		// line is the raw line read up to and including the trailing newline.
		line []byte
		// err is any error returned by the underlying reader.
		err error
	}

	// stdioActiveRequest tracks one in-flight request by its canonical JSON-RPC ID
	// so notifications/cancelled can cancel its context and suppress its reply.
	stdioActiveRequest struct {
		// cancel aborts the per-request context when notifications/cancelled arrives.
		cancel context.CancelFunc
		// cancelled reports whether the client explicitly cancelled this request.
		cancelled atomic.Bool
	}

	// stdioServerConn coordinates concurrent request dispatch, per-request
	// cancellation, and serialized newline-delimited writes on one stdio session.
	stdioServerConn struct {
		// ctx is the connection-scoped context cancelled when ServeStdio shuts down.
		ctx context.Context
		// writer owns the output stream and interrupts blocked writes on shutdown.
		writer *stdioStream
		// writeMu serializes outbound JSON-RPC responses and notifications.
		writeMu sync.Mutex
		// writeErr retains the first outbound write failure on the stream.
		writeErr error
		// writeErrCh closes when the first outbound write failure occurs.
		writeErrCh chan struct{}
		// writeErrOnce ensures writeErrCh is closed at most once.
		writeErrOnce sync.Once

		// activeMu guards the active in-flight request map.
		activeMu sync.Mutex
		// active maps canonical JSON-RPC request IDs to their cancellation state.
		active map[string]*stdioActiveRequest

		// initialized reports whether a 2025-11-25 initialize handshake succeeded.
		initialized atomic.Bool
	}

	// stdioProgressResponse validates and writes notifications/progress messages
	// for one unary stdio request while it remains active.
	stdioProgressResponse struct {
		// conn is the owning stdio server connection.
		conn *stdioServerConn
		// token is the client-supplied progressToken from params._meta.
		token json.RawMessage
		// mu serializes progress validation, sending, and request completion.
		mu sync.Mutex
		// closed reports whether the owning request handler has returned.
		closed bool
		// seen reports whether at least one progress update was sent.
		seen bool
		// last is the most recent progress value sent for monotonicity checks.
		last float64
		// failure retains the first progress write error.
		failure error
	}

	// subscriptionStdioResponse validates and writes subscription notifications
	// for one subscriptions/listen stdio request.
	subscriptionStdioResponse struct {
		// conn is the owning stdio server connection.
		conn *stdioServerConn
		// id is the marshaled JSON-RPC request ID of the listen call.
		id json.RawMessage
		// key is the canonical request ID key used to correlate subscription events.
		key string
		// state tracks acknowledgment and accepted notification filters.
		state subscriptionState
		// mu serializes notification writes and final response assembly.
		mu sync.Mutex
		// closed reports whether the listen request has finished.
		closed bool
		// failure retains the first notification write error.
		failure error
	}
)

// ServeStdio reads newline-delimited JSON-RPC messages from r, validates MCP
// protocol rules, dispatches requests concurrently through dispatch, and writes
// newline-delimited JSON-RPC responses and notifications to w. ServeStdio takes
// ownership of r and w, interrupting any blocked read or write and closing both
// descriptors before returning.
func ServeStdio(
	ctx context.Context,
	r, w *os.File,
	dispatch StdioHandlerFunc,
) error {
	if r == nil {
		return errors.New("mcp: stdio reader is required")
	}
	if w == nil {
		return errors.New("mcp: stdio writer is required")
	}
	if dispatch == nil {
		return errors.New("mcp: stdio handler is required")
	}
	inStream, err := openStdioStream(r)
	if err != nil {
		return errors.Join(err, r.Close(), w.Close())
	}
	outStream, err := openStdioStream(w)
	if err != nil {
		return errors.Join(err, inStream.close(), w.Close())
	}

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()
	stopInterrupt := context.AfterFunc(connCtx, func() {
		inStream.interrupt()
		outStream.interrupt()
	})
	defer stopInterrupt()

	conn := &stdioServerConn{
		ctx:        connCtx,
		writer:     outStream,
		writeErrCh: make(chan struct{}),
		active:     make(map[string]*stdioActiveRequest),
	}

	lines := make(chan stdioReadResult)
	readerDone := make(chan struct{})
	go readStdioLines(connCtx, inStream.pollable, lines, readerDone)

	var wg sync.WaitGroup
	var loopErr error
readLoop:
	for {
		select {
		case <-ctx.Done():
			loopErr = ctx.Err()
			break readLoop
		case <-conn.writeErrCh:
			break readLoop
		case res, ok := <-lines:
			if !ok {
				break readLoop
			}
			trimmed := bytes.TrimSpace(res.line)
			if len(trimmed) > 0 {
				conn.handleLine(connCtx, &wg, trimmed, dispatch)
			}
			if res.err != nil {
				if ctx.Err() != nil {
					loopErr = ctx.Err()
				} else if !errors.Is(res.err, io.EOF) {
					loopErr = res.err
				}
				break readLoop
			}
		}
	}

	if loopErr == nil && ctx.Err() != nil {
		loopErr = ctx.Err()
	}
	connCancel()
	stopInterrupt()
	inStream.interrupt()
	outStream.interrupt()
	<-readerDone
	wg.Wait()
	if loopErr == nil {
		loopErr = conn.writeError()
	}
	outCloseErr := outStream.close()
	inCloseErr := inStream.close()
	if loopErr != nil {
		return loopErr
	}
	if outCloseErr != nil {
		return outCloseErr
	}
	return inCloseErr
}

// ServeStdioSubscriptions binds subscription notification delivery to ctx for a
// validated subscriptions/listen stdio request and finalizes its completion or
// cancellation response.
func ServeStdioSubscriptions(
	ctx context.Context,
	req *jsonrpc.RawRequest,
	next func(context.Context) *jsonrpc.Response,
) *jsonrpc.Response {
	conn, ok := ctx.Value(stdioConnKey{}).(*stdioServerConn)
	if !ok {
		return jsonrpc.MakeErrorResponse(
			req.ID,
			jsonrpc.InternalError,
			"MCP stdio subscription requires an active stdio server connection",
			nil,
		)
	}
	id, err := json.Marshal(req.ID)
	if err != nil {
		return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, err.Error(), nil)
	}
	key, filter, err := parseSubscriptionRequest(id, req.Params)
	if err != nil {
		return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InvalidParams, err.Error(), nil)
	}
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.subscription.response")
	defer span.End()

	response := &subscriptionStdioResponse{
		conn:  conn,
		id:    cloneRaw(id),
		key:   key,
		state: subscriptionState{requested: filter},
	}
	defer response.close()

	ctx = context.WithValue(ctx, subscriptionSenderKey{}, subscriptionSender(response))
	reply := next(ctx)
	return response.finish(ctx, req.ID, reply, span)
}

// openStdioStream prepares f for interruptible stdio reads or writes.
func openStdioStream(f *os.File) (*stdioStream, error) {
	pollable, err := openPollableFile(f)
	if err != nil {
		return nil, err
	}
	return &stdioStream{
		raw:      f,
		pollable: pollable,
	}, nil
}

// readStdioLines reads newline-delimited JSON-RPC frames from r until EOF, read
// error, or context cancellation.
func readStdioLines(
	ctx context.Context,
	r io.Reader,
	lines chan<- stdioReadResult,
	done chan<- struct{},
) {
	defer close(done)
	defer close(lines)
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		select {
		case lines <- stdioReadResult{line: line, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// protocolErrorResponse constructs a JSON-RPC protocol error response for body.
func protocolErrorResponse(body []byte, failure *Error) *jsonrpc.Response {
	var request jsonrpc.RawRequest
	decodeErr := request.UnmarshalJSON(body)
	var id any
	if decodeErr == nil && !request.Invalid && request.HasID {
		id = request.ID
	}
	return jsonrpc.MakeErrorResponse(id, jsonrpc.Code(failure.Code), failure.Message, failure.Data)
}

// interrupt expires deadlines and closes the pollable descriptor to wake any
// goroutine blocked in Read or Write without waiting on writeMu.
func (s *stdioStream) interrupt() {
	s.interruptOnce.Do(func() {
		if err := s.pollable.SetDeadline(time.Now()); err != nil &&
			!errors.Is(err, os.ErrNoDeadline) &&
			!errors.Is(err, os.ErrClosed) {
			s.closeErr = err
		}
		if err := s.pollable.Close(); err != nil &&
			!errors.Is(err, os.ErrClosed) &&
			s.closeErr == nil {
			s.closeErr = err
		}
	})
}

// close interrupts the pollable descriptor and closes the underlying raw file.
func (s *stdioStream) close() error {
	s.interrupt()
	s.closeOnce.Do(func() {
		if s.raw != s.pollable {
			if err := s.raw.Close(); err != nil &&
				!errors.Is(err, os.ErrClosed) &&
				s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

// handleLine validates one raw JSON-RPC line, handles notifications, or spawns
// a tracked request worker goroutine.
func (c *stdioServerConn) handleLine(
	connCtx context.Context,
	wg *sync.WaitGroup,
	line []byte,
	dispatch StdioHandlerFunc,
) {
	legacy := isLegacyStdioRequest(line, c.initialized.Load())
	var (
		request *jsonrpc.RawRequest
		failure *Error
	)
	if legacy {
		request, failure = decodeLegacyStdioRequest(line)
	} else {
		request, failure = validateStdioRequest(line)
	}
	if failure != nil {
		response := protocolErrorResponse(line, failure)
		wg.Go(func() {
			if connCtx.Err() != nil {
				return
			}
			if err := c.writeMessage(response); err != nil {
				return
			}
		})
		return
	}
	if !request.HasID {
		if request.Method == methodCancelled {
			c.cancelRequest(request.Params)
		}
		return
	}
	if legacy && request.Method == methodInitialize {
		c.initialized.Store(true)
	}

	reqCtx, reqCancel, key, activeReq := c.startRequest(connCtx, request.ID)
	req := *request
	wg.Go(func() {
		defer c.finishRequest(key, activeReq)
		defer reqCancel()

		reqCtx = context.WithValue(reqCtx, stdioConnKey{}, c)
		reqCtx, stopProgress := c.withProgress(reqCtx, &req)
		response := dispatch(reqCtx, &req, legacy)
		stopProgress()
		if response == nil || activeReq.cancelled.Load() || connCtx.Err() != nil {
			return
		}
		if err := c.writeMessage(response); err != nil {
			return
		}
	})
}

// startRequest registers a cancellable per-request context under its canonical
// JSON-RPC request ID key.
func (c *stdioServerConn) startRequest(
	connCtx context.Context,
	id any,
) (context.Context, context.CancelFunc, string, *stdioActiveRequest) {
	reqCtx, reqCancel := context.WithCancel(connCtx)
	activeReq := &stdioActiveRequest{cancel: reqCancel}
	rawID, err := json.Marshal(id)
	if err != nil {
		return reqCtx, reqCancel, "", activeReq
	}
	key, err := protocolIDKey(rawID)
	if err != nil {
		return reqCtx, reqCancel, "", activeReq
	}
	c.activeMu.Lock()
	c.active[key] = activeReq
	c.activeMu.Unlock()
	return reqCtx, reqCancel, key, activeReq
}

// finishRequest removes activeReq from the active map when its handler exits.
func (c *stdioServerConn) finishRequest(key string, activeReq *stdioActiveRequest) {
	if key == "" {
		return
	}
	c.activeMu.Lock()
	if c.active[key] == activeReq {
		delete(c.active, key)
	}
	c.activeMu.Unlock()
}

// cancelRequest processes an inbound notifications/cancelled message and cancels
// the matching in-flight request if still active.
func (c *stdioServerConn) cancelRequest(params json.RawMessage) {
	cancelled, err := decodeSubscriptionCancellation(params)
	if err != nil {
		return
	}
	key, err := protocolIDKey(cancelled.RequestID)
	if err != nil {
		return
	}
	c.activeMu.Lock()
	activeReq := c.active[key]
	if activeReq != nil {
		delete(c.active, key)
	}
	c.activeMu.Unlock()
	if activeReq != nil {
		activeReq.cancelled.Store(true)
		activeReq.cancel()
	}
}

// withProgress attaches a request-scoped progress sender when params._meta
// carries a progressToken on a non-task, non-subscription request.
func (c *stdioServerConn) withProgress(
	ctx context.Context,
	req *jsonrpc.RawRequest,
) (context.Context, func()) {
	if req.Method == methodSubscriptionsListen || isTaskOperation(req.Method) {
		return ctx, func() {}
	}
	var fields struct {
		Meta map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP wire name.
	}
	if err := json.Unmarshal(req.Params, &fields); err != nil {
		return ctx, func() {}
	}
	token, present := fields.Meta["progressToken"]
	if !present {
		return ctx, func() {}
	}
	sender := &stdioProgressResponse{
		conn:  c,
		token: cloneRaw(token),
	}
	return context.WithValue(ctx, progressSenderKey{}, progressSender(sender.send)), sender.close
}

// writeNotification encodes and writes a server-initiated JSON-RPC notification.
func (c *stdioServerConn) writeNotification(method string, params any) error {
	return c.writeMessage(rpcNotification{
		JSONRPC: rpcVersion,
		Method:  method,
		Params:  params,
	})
}

// writeMessage marshals message as compact JSON and writes it as one line.
func (c *stdioServerConn) writeMessage(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode MCP stdio message: %w", err)
	}
	return c.writeLine(data)
}

// writeLine writes one newline-terminated frame to the connection's output
// stream while recording the first write failure.
func (c *stdioServerConn) writeLine(data []byte) error {
	line := append(bytes.TrimSpace(data), '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeErr != nil {
		return c.writeErr
	}
	if _, err := c.writer.pollable.Write(line); err != nil {
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		c.writeErr = fmt.Errorf("write MCP stdio message: %w", err)
		c.writeErrOnce.Do(func() {
			close(c.writeErrCh)
		})
		return c.writeErr
	}
	return nil
}

// writeError returns the first recorded write error on the connection, if any.
func (c *stdioServerConn) writeError() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeErr
}

// send validates and writes one notifications/progress update for the active
// unary request.
func (r *stdioProgressResponse) send(
	ctx context.Context,
	value float64,
	total *float64,
	message *string,
) error {
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
	if err := r.conn.writeNotification("notifications/progress", progressWire{
		Token:   r.token,
		Value:   &value,
		Total:   total,
		Message: message,
	}); err != nil {
		r.failure = err
		return err
	}
	r.seen, r.last = true, value
	trace.SpanFromContext(ctx).AddEvent(
		"mcp.progress.sent",
		trace.WithAttributes(attribute.Float64("progress", value)),
	)
	return nil
}

// close marks the progress sender finished when the request handler returns.
func (r *stdioProgressResponse) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

// send validates and writes one subscription event notification over stdio.
func (r *subscriptionStdioResponse) send(
	ctx context.Context,
	kind SubscriptionEventKind,
	accepted *SubscriptionFilter,
	uri string,
	task map[string]json.RawMessage,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writable(ctx); err != nil {
		return err
	}
	data, err := encodeSubscriptionNotification(&r.state, r.id, kind, accepted, uri, task)
	if err != nil {
		return err
	}
	if err := r.conn.writeLine(data); err != nil {
		r.failure = err
		return err
	}
	trace.SpanFromContext(ctx).AddEvent(
		"mcp.subscription.sent",
		trace.WithAttributes(attribute.String("notification.method", string(kind))),
	)
	return nil
}

// finish finalizes the subscriptions/listen response or emits a cancellation
// notification when the source fails after acknowledging the subscription.
func (r *subscriptionStdioResponse) finish(
	ctx context.Context,
	reqID any,
	reply *jsonrpc.Response,
	span trace.Span,
) *jsonrpc.Response {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writable(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil
	}
	r.closed = true
	if reply == nil {
		err := errors.New("generated MCP handler did not write a final JSON-RPC response")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return jsonrpc.MakeErrorResponse(reqID, jsonrpc.InternalError, err.Error(), nil)
	}
	if reply.Error != nil {
		if !r.state.acknowledged {
			return reply
		}
		reason := reply.Error.Message
		if err := r.conn.writeNotification(methodCancelled, struct {
			RequestID json.RawMessage `json:"requestId"` //nolint:tagliatelle // MCP wire name.
			Reason    string          `json:"reason"`
		}{
			RequestID: r.id,
			Reason:    reason,
		}); err != nil {
			span.RecordError(err)
		}
		return nil
	}
	rawResult, err := json.Marshal(reply.Result)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return jsonrpc.MakeErrorResponse(reqID, jsonrpc.InternalError, err.Error(), nil)
	}
	completed, err := completeSubscriptionResult(&r.state, r.id, rawResult)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		var failure *Error
		if errors.As(err, &failure) {
			return jsonrpc.MakeErrorResponse(reqID, jsonrpc.Code(failure.Code), failure.Message, failure.Data)
		}
		return jsonrpc.MakeErrorResponse(reqID, jsonrpc.InternalError, err.Error(), nil)
	}
	return jsonrpc.MakeSuccessResponse(reqID, completed)
}

// writable checks whether the subscription stream is still open and uncancelled.
func (r *subscriptionStdioResponse) writable(ctx context.Context) error {
	if r.failure != nil {
		return r.failure
	}
	if r.closed {
		return errors.New("MCP subscription request has finished")
	}
	return ctx.Err()
}

// close marks the subscription sender finished when ServeStdioSubscriptions exits.
func (r *subscriptionStdioResponse) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}
