// This file connects to an MCP server over HTTP. It completes initialization,
// sends the selected protocol version and session identifier on later requests,
// and reads tool results returned as JSON or server-sent events.

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type (
	// HTTPOptions configures the HTTP caller.
	HTTPOptions struct {
		// Endpoint is the HTTP URL that accepts MCP JSON-RPC requests.
		Endpoint string
		// Client sends the HTTP requests. A client with a 30-second timeout is used
		// when Client is nil.
		Client *http.Client
		// ClientInfo identifies this application to the MCP server.
		ClientInfo ClientInfo
		// InitTimeout limits the initialize request when it is greater than zero.
		InitTimeout time.Duration
	}

	// HTTPCaller implements Caller over JSON-RPC HTTP.
	HTTPCaller struct {
		transport *httpTransport
	}

	// httpTransport sends JSON-RPC requests for HTTPCaller.
	httpTransport struct {
		endpoint        string
		session         *HTTPSession
		protocolVersion string
		clientInfo      ClientInfo
		initTimeout     time.Duration
		initializeMu    sync.Mutex
		id              uint64
	}

	httpStatusError struct {
		status int
	}
)

// DefaultProtocolVersion is the MCP protocol version implemented by the handwritten callers.
const DefaultProtocolVersion = "2025-06-18"

// NewHTTPCaller creates an HTTP caller and performs the MCP initialize handshake.
func NewHTTPCaller(ctx context.Context, opts HTTPOptions) (*HTTPCaller, error) {
	if err := opts.ClientInfo.Validate(); err != nil {
		return nil, err
	}
	transport, err := newHTTPTransport(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &HTTPCaller{transport: transport}, nil
}

// CallTool invokes tools/call over HTTP and normalizes the response.
func (c *HTTPCaller) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	if !c.transport.session.Initialized() {
		if err := c.transport.startNewSession(ctx); err != nil {
			return CallResponse{}, fmt.Errorf("start new MCP session: %w", err)
		}
	}
	params := map[string]any{
		"name":      req.Tool,
		"arguments": req.Payload,
	}
	addTraceMeta(ctx, params)
	var result toolsCallResult
	if err := c.transport.call(ctx, "tools/call", params, &result); err != nil {
		if !c.transport.session.Initialized() {
			if initializeErr := c.transport.startNewSession(ctx); initializeErr != nil {
				return CallResponse{}, errors.Join(err, fmt.Errorf("start new MCP session: %w", initializeErr))
			}
		}
		return CallResponse{}, err
	}
	return normalizeToolResult(result)
}

// Error reports the HTTP status returned by the MCP endpoint.
func (e *httpStatusError) Error() string {
	return fmt.Sprintf("mcp rpc status %d", e.status)
}

// newHTTPTransport checks the endpoint, sends initialize, and returns the
// connection state used for later tool calls.
func newHTTPTransport(ctx context.Context, opts HTTPOptions) (*httpTransport, error) {
	if opts.Endpoint == "" {
		return nil, errors.New("mcp: HTTP endpoint is required")
	}
	parsed, parseErr := url.Parse(opts.Endpoint)
	if parseErr != nil || parsed.Host == "" ||
		!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("mcp: invalid HTTP endpoint %q", opts.Endpoint)
	}
	endpoint := parsed.String()
	httpClient := opts.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	transport := &httpTransport{
		endpoint:        endpoint,
		protocolVersion: DefaultProtocolVersion,
		clientInfo:      opts.ClientInfo,
		initTimeout:     opts.InitTimeout,
	}
	transport.session = NewHTTPSession(httpClient, DefaultProtocolVersion)
	if err := transport.initialize(ctx); err != nil {
		return nil, err
	}
	return transport, nil
}

// initialize serializes the initial handshake so no other handshake can
// replace its session state before the initialized notification is accepted.
func (t *httpTransport) initialize(ctx context.Context) error {
	t.initializeMu.Lock()
	defer t.initializeMu.Unlock()
	return t.initializeLocked(ctx)
}

// startNewSession starts a handshake only when the 404 response still left the
// session inactive. Another rejected call may already have replaced it.
func (t *httpTransport) startNewSession(ctx context.Context) error {
	t.initializeMu.Lock()
	defer t.initializeMu.Unlock()
	if t.session.Initialized() {
		return nil
	}
	return t.initializeLocked(ctx)
}

// initializeLocked sends initialize without session headers, then sends the
// initialized notification with the protocol and session selected by the server.
func (t *httpTransport) initializeLocked(ctx context.Context) error {
	initCtx := ctx
	if t.initTimeout > 0 {
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, t.initTimeout)
		defer cancel()
	}
	t.session.Reset()
	payload := map[string]any{
		"protocolVersion": t.protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    t.clientInfo.Name,
			"version": t.clientInfo.Version,
		},
	}
	var result initializeResult
	if err := t.call(initCtx, "initialize", payload, &result); err != nil {
		t.session.Reset()
		return fmt.Errorf("mcp initialize failed: %w", err)
	}
	if err := validateInitializeResult(result); err != nil {
		t.session.Reset()
		return fmt.Errorf("mcp initialize failed: %w", err)
	}
	t.session.Begin()
	if err := t.notify(initCtx, rpcMethodInitialized, map[string]any{}); err != nil {
		t.session.Reset()
		return fmt.Errorf("mcp initialize failed: %w", err)
	}
	return nil
}

// nextID returns the next JSON-RPC request number for this connection.
func (t *httpTransport) nextID() uint64 {
	return atomic.AddUint64(&t.id, 1)
}

// call sends one JSON-RPC request and decodes its result into result when the
// caller expects a response body.
func (t *httpTransport) call(ctx context.Context, method string, params any, result any) (err error) {
	id := t.nextID()
	reqBody := rpcRequest{JSONRPC: rpcVersion, Method: method, ID: id, Params: params}
	resp, err := t.send(ctx, reqBody)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, resp.Body.Close())
	}()
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{status: resp.StatusCode}
	}
	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return NewMalformedResponseError(err)
	}
	if rpcResp.Error != nil {
		return rpcResp.Error.callerError()
	}
	if result != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return NewMalformedResponseError(err)
		}
	}
	return nil
}

// notify sends one JSON-RPC notification and closes the HTTP response without
// decoding a JSON-RPC response, because notifications never receive one.
func (t *httpTransport) notify(ctx context.Context, method string, params any) (err error) {
	message := rpcNotification{JSONRPC: rpcVersion, Method: method, Params: params}
	resp, err := t.send(ctx, message)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, resp.Body.Close())
	}()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("mcp rpc status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return NewMalformedResponseError(err)
	}
	if len(body) > 0 {
		return errors.New("mcp: notification response must be empty")
	}
	return nil
}

// send writes one JSON-RPC message to the configured HTTP endpoint and
// returns the HTTP response for the caller to handle according to the message.
func (t *httpTransport) send(ctx context.Context, message any) (*http.Response, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return nil, NewInternalError(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, NewInternalError(err)
	}
	req.Header.Set("Content-Type", "application/json")
	injectTraceHeaders(ctx, req.Header)
	// #nosec G704 -- MCP endpoint is provided by the caller; transport must perform the request.
	return t.session.Do(req)
}
