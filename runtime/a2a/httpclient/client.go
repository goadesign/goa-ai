package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"goa.design/goa-ai/runtime/a2a"
)

type (
	// Option configures the HTTP client.
	Option func(*Client)

	// Client implements the a2a.Caller interface over JSON-RPC HTTP.
	Client struct {
		endpoint string
		http     *http.Client
		headers  http.Header
		id       uint64
	}

	rpcRequest struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		ID      uint64 `json:"id"`
		Params  any    `json:"params,omitempty"`
	}

	rpcResponse struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
		ID      uint64          `json:"id"`
	}

	rpcError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
)

// Error converts the rpcError into a human-readable string.
func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("a2a error %d: %s", e.Code, e.Message)
}

// callerError converts the rpcError into the public a2a.Error type.
func (e *rpcError) callerError() *a2a.Error {
	if e == nil {
		return nil
	}
	return &a2a.Error{Code: e.Code, Message: e.Message}
}

// WithHTTPClient overrides the underlying *http.Client used for requests.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) {
		cl.http = c
	}
}

// WithHeader adds a static header to all outgoing requests.
func WithHeader(name, value string) Option {
	return func(cl *Client) {
		if cl.headers == nil {
			cl.headers = make(http.Header)
		}
		cl.headers.Add(name, value)
	}
}

// WithBearerToken configures the client to send an Authorization Bearer token.
func WithBearerToken(token string) Option {
	return WithHeader("Authorization", "Bearer "+token)
}

// New constructs a new Client implementing a2a.Caller. The endpoint must point
// to the A2A JSON-RPC URL (for example, "https://host.example.com/a2a").
func New(endpoint string, opts ...Option) (*Client, error) {
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080/a2a"
	}
	cl := &Client{
		endpoint: endpoint,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		headers: make(http.Header),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cl)
		}
	}
	if cl.http == nil {
		cl.http = &http.Client{Timeout: 30 * time.Second}
	}
	return cl, nil
}

// Ensure Client implements a2a.Caller.
var _ a2a.Caller = (*Client)(nil)

func (c *Client) nextID() uint64 {
	return atomic.AddUint64(&c.id, 1)
}

// SendTask invokes the tasks/send method on the remote A2A endpoint. It
// forwards the suite, skill, and payload without applying any string
// manipulation to the identifiers.
func (c *Client) SendTask(ctx context.Context, req a2a.SendTaskRequest) (a2a.SendTaskResponse, error) {
	id := c.nextID()
	params := map[string]any{
		"suite":   req.Suite,
		"skill":   req.Skill,
		"payload": json.RawMessage(req.Payload),
	}
	rpcReq := rpcRequest{
		JSONRPC: "2.0",
		Method:  "tasks/send",
		ID:      id,
		Params:  params,
	}
	body, err := json.Marshal(rpcReq)
	if err != nil {
		return a2a.SendTaskResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return a2a.SendTaskResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, vs := range c.headers {
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return a2a.SendTaskResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return a2a.SendTaskResponse{}, fmt.Errorf("a2a http status %d", resp.StatusCode)
	}

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return a2a.SendTaskResponse{}, err
	}
	if rpcResp.Error != nil {
		return a2a.SendTaskResponse{}, rpcResp.Error.callerError()
	}

	return a2a.SendTaskResponse{
		Result: rpcResp.Result,
	}, nil
}
