package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// HTTPOptions configures the HTTP Caller.
type HTTPOptions struct {
	Endpoint        string
	Client          *http.Client
	ProtocolVersion string
	ClientName      string
	ClientVersion   string
	InitTimeout     time.Duration
}

// DefaultProtocolVersion is the MCP protocol version used when none is provided.
const DefaultProtocolVersion = "2024-11-05"

// HTTPCaller implements Caller over JSON-RPC HTTP.
type HTTPCaller struct {
	transport *httpTransport
}

// NewHTTPCaller creates an HTTP-based Caller and performs MCP initialize handshake.
func NewHTTPCaller(ctx context.Context, opts HTTPOptions) (*HTTPCaller, error) {
	transport, err := newHTTPTransport(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &HTTPCaller{transport: transport}, nil
}

// CallTool invokes tools/call over HTTP and normalizes the response.
func (c *HTTPCaller) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	params := map[string]any{
		"name":      req.Tool,
		"arguments": req.Payload,
	}
	addTraceMeta(ctx, params)
	var result toolsCallResult
	if err := c.transport.call(ctx, "tools/call", params, &result); err != nil {
		return CallResponse{}, err
	}
	return normalizeToolResult(result)
}

// httpTransport shares JSON-RPC HTTP plumbing across different callers (HTTP, SSE).
type httpTransport struct {
	endpoint string
	client   *http.Client
	id       uint64
}

func newHTTPTransport(ctx context.Context, opts HTTPOptions) (*httpTransport, error) {
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080/rpc"
	}
	httpClient := opts.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	transport := &httpTransport{endpoint: endpoint, client: httpClient}
	initCtx := ctx
	if opts.InitTimeout > 0 {
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, opts.InitTimeout)
		defer cancel()
	}
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
	if err := transport.call(initCtx, "initialize", payload, nil); err != nil {
		return nil, fmt.Errorf("mcp initialize failed: %w", err)
	}
	return transport, nil
}

func (t *httpTransport) nextID() uint64 {
	return atomic.AddUint64(&t.id, 1)
}

func (t *httpTransport) call(ctx context.Context, method string, params any, result any) error {
	id := t.nextID()
	reqBody := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		ID:      id,
		Params:  params,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	injectTraceHeaders(ctx, req.Header)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mcp rpc status %d", resp.StatusCode)
	}
	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return rpcResp.Error.callerError()
	}
	if result != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return err
		}
	}
	return nil
}
