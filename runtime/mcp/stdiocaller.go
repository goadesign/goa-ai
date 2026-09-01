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
	"time"
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
		// InitTimeout limits initialization when it is greater than zero.
		InitTimeout time.Duration
	}

	// StdioCaller implements Caller using the MCP stdio transport.
	StdioCaller struct {
		cmd         *exec.Cmd
		stdin       io.WriteCloser
		pending     map[uint64]chan callResult
		pendingMu   sync.Mutex
		writeMu     sync.Mutex
		nextID      uint64
		closed      chan struct{}
		closeOnce   sync.Once
		shutdownErr error
		closeErr    error
		closeErrMu  sync.Mutex
	}

	callResult struct {
		resp rpcResponse
		err  error
	}
)

// NewStdioCaller launches the target command, performs the MCP initialize handshake,
// and returns a Caller that keeps the stdio session alive across tool invocations.
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
	//nolint:gosec,noctx // The constructor context covers initialization; Close owns the process lifetime.
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
	caller := &StdioCaller{cmd: cmd, stdin: stdin, pending: make(map[uint64]chan callResult), closed: make(chan struct{})}
	go caller.readLoop(stdout)
	if err := caller.initialize(ctx, opts); err != nil {
		return nil, errors.Join(err, caller.Close())
	}
	return caller, nil
}

// Close terminates the stdio process and releases resources.
func (c *StdioCaller) Close() error {
	c.closeOnce.Do(func() {
		var closeErr error
		if c.stdin != nil {
			if err := c.stdin.Close(); err != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("close MCP server input: %w", err))
			}
		}
		killed := false
		if c.cmd != nil && c.cmd.ProcessState == nil {
			if err := c.cmd.Process.Kill(); err == nil {
				killed = true
			} else if !errors.Is(err, os.ErrProcessDone) {
				closeErr = errors.Join(closeErr, fmt.Errorf("stop MCP server: %w", err))
			}
		}
		if c.cmd != nil {
			if err := c.cmd.Wait(); err != nil {
				var exitErr *exec.ExitError
				if !killed || !errors.As(err, &exitErr) {
					closeErr = errors.Join(closeErr, fmt.Errorf("wait for MCP server: %w", err))
				}
			}
		}
		c.shutdownErr = closeErr
		close(c.closed)
	})
	return c.shutdownErr
}

// CallTool invokes tools/call over the stdio transport.
func (c *StdioCaller) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	params := map[string]any{"name": req.Tool, "arguments": req.Payload}
	addTraceMeta(ctx, params)
	var result toolsCallResult
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return CallResponse{}, err
	}
	return normalizeToolResult(result)
}

// initialize sends the client identity and requested protocol version to the
// server process before any tool call can run.
func (c *StdioCaller) initialize(ctx context.Context, opts StdioOptions) error {
	payload := map[string]any{
		"protocolVersion": DefaultProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    opts.ClientInfo.Name,
			"version": opts.ClientInfo.Version,
		},
	}
	initCtx := ctx
	if opts.InitTimeout > 0 {
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, opts.InitTimeout)
		defer cancel()
	}
	var result initializeResult
	if err := c.call(initCtx, "initialize", payload, &result); err != nil {
		return err
	}
	if err := validateInitializeResult(result); err != nil {
		return err
	}
	return c.notify(rpcMethodInitialized, map[string]any{})
}

// call writes one request and waits for the read loop to return the response
// with the same request number.
func (c *StdioCaller) call(ctx context.Context, method string, params any, result any) error {
	id := c.next()
	ch := make(chan callResult, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	req := rpcRequest{JSONRPC: rpcVersion, Method: method, ID: id, Params: params}
	if err := c.writeMessage(req); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case res := <-ch:
		if res.err != nil {
			return res.err
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
		c.removePending(id)
		return ctx.Err()
	case <-c.closed:
		return c.closeError()
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
			if incoming.Method == "" || len(incoming.ID) == 0 {
				continue
			}
			if err := c.replyToServerRequest(incoming); err != nil {
				c.failPending(err)
				return
			}
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.pendingMu.Unlock()
		if ok {
			ch <- callResult{resp: resp}
			close(ch)
		}
	}
}

// replyToServerRequest answers ping and rejects methods this client did not
// advertise, preserving the string or numeric identifier sent by the server.
func (c *StdioCaller) replyToServerRequest(message rpcMessage) error {
	reply := rpcReply{JSONRPC: rpcVersion, ID: message.ID}
	if message.Method == "ping" {
		reply.Result = json.RawMessage(`{}`)
	} else {
		reply.Error = &rpcError{Code: JSONRPCMethodNotFound, Message: "method not found"}
	}
	return c.writeMessage(reply)
}

// failPending returns the stream failure to every waiting call and closes the
// server process.
func (c *StdioCaller) failPending(err error) {
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- callResult{err: err}
		close(ch)
	}
	c.pendingMu.Unlock()
	c.setCloseError(err)
	if closeErr := c.Close(); closeErr != nil {
		c.setCloseError(closeErr)
	}
}

// removePending stops waiting for the request after its context ends or its
// request cannot be written.
func (c *StdioCaller) removePending(id uint64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
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
