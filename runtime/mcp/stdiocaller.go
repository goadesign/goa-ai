// This file starts an MCP server process and exchanges one JSON message per
// line over standard input and output while the agent runtime calls tools.

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type (
	// StdioOptions configures the stdio-based MCP caller.
	StdioOptions struct {
		// Command is the MCP server executable.
		Command string
		// Args are passed to Command.
		Args []string
		// Env adds environment variables to the current process environment.
		Env []string
		// Dir is the working directory for Command.
		Dir string
		// ClientInfo identifies this application to the MCP server.
		ClientInfo ClientInfo
		// InputSupport names the interactions the host can fulfill.
		InputSupport InputSupport
	}

	// StdioCaller implements Caller using the MCP stdio transport.
	StdioCaller struct {
		clientInfo   ClientInfo
		inputSupport InputSupport
		cmd          *exec.Cmd
		stdin        io.WriteCloser
		pending      map[uint64]*pendingCall
		progress     map[string]*pendingCall
		pendingMu    sync.Mutex
		writeMu      sync.Mutex
		nextID       uint64
		closed       chan struct{}
		closeOnce    sync.Once
		shutdownErr  error
		closeErr     error
		closeErrMu   sync.Mutex
	}

	callResult struct {
		resp     rpcResponse
		progress *progressWire
	}
	pendingCall struct {
		ctx      context.Context
		results  chan callResult
		progress *progressReceiver
	}
)

// NewStdioCaller launches the target command, and returns a caller without
// initialization. Each invocation carries its own identity and capabilities.
func NewStdioCaller(ctx context.Context, opts StdioOptions) (*StdioCaller, error) {
	if err := opts.ClientInfo.Validate(); err != nil {
		return nil, err
	}
	if opts.Command == "" {
		return nil, errors.New("command is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	//nolint:gosec,noctx // The constructor context covers process startup; Close owns the process lifetime.
	cmd := exec.Command(opts.Command, opts.Args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open MCP server input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closeErr := stdin.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close MCP server input: %w", closeErr)
		}
		return nil, errors.Join(fmt.Errorf("open MCP server output: %w", err), closeErr)
	}
	if err := cmd.Start(); err != nil {
		stdinErr := stdin.Close()
		if stdinErr != nil {
			stdinErr = fmt.Errorf("close MCP server input: %w", stdinErr)
		}
		stdoutErr := stdout.Close()
		if stdoutErr != nil {
			stdoutErr = fmt.Errorf("close MCP server output: %w", stdoutErr)
		}
		return nil, errors.Join(fmt.Errorf("start MCP server: %w", err), stdinErr, stdoutErr)
	}
	caller := &StdioCaller{clientInfo: opts.ClientInfo,
		inputSupport: opts.InputSupport, cmd: cmd, stdin: stdin, pending: make(map[uint64]*pendingCall), progress: make(map[string]*pendingCall), closed: make(chan struct{})}
	go caller.readLoop(stdout)
	return caller, nil
}

// Close closes server input and waits for a clean exit. When ctx ends, it kills
// the process and waits for it to be reaped before returning.
func (c *StdioCaller) Close(ctx context.Context) error {
	c.closeOnce.Do(func() {
		var closeErr error
		if c.stdin != nil {
			if err := c.stdin.Close(); err != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("close MCP server input: %w", err))
			}
		}
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("wait for MCP server: %w", err))
			}
		case <-ctx.Done():
			killErr := c.cmd.Process.Kill()
			if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				closeErr = errors.Join(closeErr, fmt.Errorf("stop MCP server: %w", killErr))
			}
			waitErr := <-done
			var exitErr *exec.ExitError
			if waitErr != nil && !errors.As(waitErr, &exitErr) {
				closeErr = errors.Join(closeErr, fmt.Errorf("wait for MCP server: %w", waitErr))
			}
		}
		c.shutdownErr = closeErr
		c.setCloseError(errors.New("MCP stdio caller closed"))
		close(c.closed)
	})
	return c.shutdownErr
}

// CallTool invokes tools/call over the stdio transport.
func (c *StdioCaller) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	params := toolParams(req)
	var result toolsCallResult
	if err := c.call(ctx, methodToolsCall, params, &result); err != nil {
		return CallResponse{}, err
	}
	return normalizeCallResult(result, c.inputSupport)
}

// call writes one request and waits for the read loop to return the response
// with the same request number.
func (c *StdioCaller) call(ctx context.Context, method string, params map[string]any, result any) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.stdio.request")
	span.SetAttributes(attribute.String("rpc.method", method))
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
	meta, err := requestMeta(ctx, c.clientInfo, c.inputSupport, nil)
	if err != nil {
		return err
	}
	id := c.next()
	encodedID, err := json.Marshal(id)
	if err != nil {
		return NewInternalError(err)
	}
	receiver, err := newProgressReceiver(ctx, encodedID, meta)
	if err != nil {
		return NewInternalError(err)
	}
	params["_meta"] = meta
	pending := &pendingCall{ctx: ctx, results: make(chan callResult, 1), progress: receiver}
	c.pendingMu.Lock()
	c.pending[id] = pending
	if receiver != nil {
		c.progress[receiver.token] = pending
	}
	c.pendingMu.Unlock()
	req := rpcRequest{JSONRPC: rpcVersion, Method: method, ID: id, Params: params}
	if method == methodToolsCall {
		defer func() { err = unknownToolOutcome(err) }()
	}
	if err := c.writeMessage(req); err != nil {
		c.removePending(id)
		return err
	}
	for {
		select {
		case res := <-pending.results:
			if res.progress != nil {
				if err := receiver.accept(ctx, res.progress); err != nil {
					c.removePending(id)
					cancelErr := c.notify("notifications/cancelled", map[string]any{"requestId": id})
					return errors.Join(err, cancelErr)
				}
				continue
			}
			if res.resp.Error != nil {
				return res.resp.Error.callerError()
			}
			if result != nil && res.resp.Result != nil {
				if err := json.Unmarshal(res.resp.Result, result); err != nil {
					return NewMalformedResponseError(err)
				}
			}
			return nil
		case <-ctx.Done():
			if c.removePending(id) {
				err := c.notify("notifications/cancelled", map[string]any{"requestId": id})
				return errors.Join(ctx.Err(), err)
			}
			return ctx.Err()
		case <-c.closed:
			return c.closeError()
		}
	}
}

// notify writes one notification without adding a response waiter.
func (c *StdioCaller) notify(method string, params any) error {
	return c.writeMessage(rpcNotification{JSONRPC: rpcVersion, Method: method, Params: params})
}

// writeMessage writes one compact JSON-RPC message followed by a newline so
// the server process can read exactly one message at a time.
func (c *StdioCaller) writeMessage(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return NewInternalError(err)
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("write MCP message: %w", err)
	}
	return nil
}

// readLoop reads complete responses and sends each one to the waiting call.
func (c *StdioCaller) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		message, err := reader.ReadBytes('\n')
		if err != nil {
			c.failPending(err)
			return
		}
		var incoming rpcMessage
		if err := json.Unmarshal(message, &incoming); err != nil {
			c.failPending(NewMalformedResponseError(err))
			return
		}
		resp, ok, err := incoming.numericResponse()
		if err != nil {
			c.failPending(err)
			return
		}
		if !ok {
			if len(incoming.ID) > 0 {
				c.failPending(NewMalformedResponseError(errors.New("independent server requests are not supported by this protocol")))
				return
			}
			if incoming.Method == "notifications/progress" {
				update, err := decodeProgress(incoming.Params)
				if err != nil {
					c.failPending(NewMalformedResponseError(err))
					return
				}
				if err := c.deliverProgress(update); err != nil {
					c.failPending(err)
					return
				}
			}
			continue
		}
		c.pendingMu.Lock()
		pending, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
			if pending.progress != nil {
				delete(c.progress, pending.progress.token)
			}
		}
		c.pendingMu.Unlock()
		if ok {
			select {
			case pending.results <- callResult{resp: resp}:
			case <-pending.ctx.Done():
			case <-c.closed:
			}
		}
	}
}

// failPending returns the stream failure to every waiting call and closes the
// server process.
func (c *StdioCaller) failPending(err error) {
	c.setCloseError(err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if closeErr := c.Close(ctx); closeErr != nil {
		c.setCloseError(closeErr)
	}
	c.pendingMu.Lock()
	clear(c.pending)
	clear(c.progress)
	c.pendingMu.Unlock()
}

// deliverProgress sends one update to its operation's goroutine. The single
// waiting slot applies backpressure without running host callbacks in the read
// loop. Cancellation releases a blocked send; late tokens reach no other call.
func (c *StdioCaller) deliverProgress(update *progressWire) error {
	key, err := progressTokenKey(update.Token)
	if err != nil {
		return NewMalformedResponseError(err)
	}
	c.pendingMu.Lock()
	pending := c.progress[key]
	c.pendingMu.Unlock()
	if pending == nil {
		return nil
	}
	select {
	case pending.results <- callResult{progress: update}:
	case <-pending.ctx.Done():
	case <-c.closed:
	}
	return nil
}

// removePending stops waiting for the request after its context ends or its
// request cannot be written.
func (c *StdioCaller) removePending(id uint64) bool {
	c.pendingMu.Lock()
	pending, present := c.pending[id]
	if present && pending.progress != nil {
		delete(c.progress, pending.progress.token)
	}
	delete(c.pending, id)
	c.pendingMu.Unlock()
	return present
}

// next returns the next JSON-RPC request number for this process.
func (c *StdioCaller) next() uint64 {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.nextID++
	return c.nextID
}

// setCloseError records the first read failure returned to calls that observe
// the closed process.
func (c *StdioCaller) setCloseError(err error) {
	if err == nil {
		return
	}
	c.closeErrMu.Lock()
	if c.closeErr == nil {
		c.closeErr = err
	}
	c.closeErrMu.Unlock()
}

// closeError returns the read failure that ended the process connection.
func (c *StdioCaller) closeError() error {
	c.closeErrMu.Lock()
	defer c.closeErrMu.Unlock()
	if c.closeErr == nil {
		return errors.New("stdio caller closed")
	}
	return c.closeErr
}
