package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StdioOptions configures the stdio-based MCP caller.
type StdioOptions struct {
	Command         string
	Args            []string
	Env             []string
	Dir             string
	ProtocolVersion string
	ClientName      string
	ClientVersion   string
	InitTimeout     time.Duration
}

// StdioCaller implements Caller using the MCP stdio transport.
type StdioCaller struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	pending    map[uint64]chan callResult
	pendingMu  sync.Mutex
	writeMu    sync.Mutex
	nextID     uint64
	closed     chan struct{}
	closeOnce  sync.Once
	closeErr   error
	closeErrMu sync.Mutex
}

type callResult struct {
	resp rpcResponse
	err  error
}

// NewStdioCaller launches the target command, performs the MCP initialize handshake,
// and returns a Caller that keeps the stdio session alive across tool invocations.
func NewStdioCaller(ctx context.Context, opts StdioOptions) (*StdioCaller, error) {
	if opts.Command == "" {
		return nil, errors.New("command is required")
	}
	cmd := exec.CommandContext(ctx, opts.Command, opts.Args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	caller := &StdioCaller{
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[uint64]chan callResult),
		closed:  make(chan struct{}),
	}
	go caller.readLoop(stdout)
	if stderr != nil {
		go io.Copy(io.Discard, stderr)
	}
	if err := caller.initialize(ctx, opts); err != nil {
		_ = caller.Close()
		return nil, err
	}
	return caller, nil
}

// Close terminates the stdio process and releases resources.
func (c *StdioCaller) Close() error {
	c.closeOnce.Do(func() {
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.ProcessState == nil {
			_ = c.cmd.Process.Kill()
		}
		if c.cmd != nil {
			_ = c.cmd.Wait()
		}
		close(c.closed)
	})
	return nil
}

// CallTool invokes tools/call over the stdio transport.
func (c *StdioCaller) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	params := map[string]any{
		"name":      req.Tool,
		"arguments": json.RawMessage(req.Payload),
	}
	addTraceMeta(ctx, params)
	var result toolsCallResult
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return CallResponse{}, err
	}
	return normalizeToolResult(result)
}

func (c *StdioCaller) initialize(ctx context.Context, opts StdioOptions) error {
	protocol := opts.ProtocolVersion
	if protocol == "" {
		protocol = DefaultProtocolVersion
	}
	clientName := opts.ClientName
	if clientName == "" {
		clientName = "goa-ai"
	}
	clientVersion := opts.ClientVersion
	if clientVersion == "" {
		clientVersion = "dev"
	}
	payload := map[string]any{
		"protocolVersion": protocol,
		"clientInfo": map[string]any{
			"name":    clientName,
			"version": clientVersion,
		},
	}
	initCtx := ctx
	if opts.InitTimeout > 0 {
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, opts.InitTimeout)
		defer cancel()
	}
	return c.call(initCtx, "initialize", payload, nil)
}

func (c *StdioCaller) call(ctx context.Context, method string, params any, result any) error {
	id := c.next()
	ch := make(chan callResult, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	req := rpcRequest{JSONRPC: "2.0", Method: method, ID: id, Params: params}
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
				return err
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

func (c *StdioCaller) writeMessage(req rpcRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return err
	}
	if _, err := c.stdin.Write(data); err != nil {
		return err
	}
	return nil
}

func (c *StdioCaller) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		frame, err := readFrame(reader)
		if err != nil {
			c.failPending(err)
			return
		}
		var resp rpcResponse
		if err := json.Unmarshal(frame, &resp); err != nil {
			continue
		}
		if resp.ID == 0 {
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

func (c *StdioCaller) failPending(err error) {
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- callResult{err: err}
		close(ch)
	}
	c.pendingMu.Unlock()
	c.setCloseError(err)
	_ = c.Close()
}

func (c *StdioCaller) removePending(id uint64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *StdioCaller) next() uint64 {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.nextID++
	return c.nextID
}

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

func (c *StdioCaller) closeError() error {
	c.closeErrMu.Lock()
	defer c.closeErrMu.Unlock()
	if c.closeErr == nil {
		return errors.New("stdio caller closed")
	}
	return c.closeErr
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if length < 0 {
				continue
			}
			break
		}
		if after, ok := strings.CutPrefix(strings.ToLower(line), "content-length:"); ok {
			val := strings.TrimSpace(after)
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, err
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("content-length header missing")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
