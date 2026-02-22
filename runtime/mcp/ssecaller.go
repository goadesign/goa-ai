package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SSECaller implements Caller using HTTP SSE streams for tools/call.
type SSECaller struct{ transport *httpTransport }

// NewSSECaller creates an SSE-based Caller and performs the MCP initialize handshake.
func NewSSECaller(ctx context.Context, opts HTTPOptions) (*SSECaller, error) {
	transport, err := newHTTPTransport(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &SSECaller{transport: transport}, nil
}

// CallTool invokes tools/call via SSE and normalizes the final response.
func (c *SSECaller) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	params := map[string]any{
		"name":      req.Tool,
		"arguments": req.Payload,
	}
	addTraceMeta(ctx, params)
	rpcReq := rpcRequest{JSONRPC: "2.0", Method: "tools/call", ID: c.transport.nextID(), Params: params}
	body, err := json.Marshal(rpcReq)
	if err != nil {
		return CallResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.transport.endpoint, bytes.NewReader(body))
	if err != nil {
		return CallResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	injectTraceHeaders(ctx, httpReq.Header)
	resp, err := c.transport.client.Do(httpReq) //nolint:gosec // endpoint is URL-parsed and validated in newHTTPTransport
	if err != nil {
		return CallResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return CallResponse{}, fmt.Errorf("mcp rpc status %d: %s", resp.StatusCode, string(raw))
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.HasPrefix(ct, "text/event-stream") {
		raw, _ := io.ReadAll(resp.Body)
		ctVal := resp.Header.Get("Content-Type")
		return CallResponse{}, fmt.Errorf("unexpected content type %q: %s", ctVal, string(raw))
	}
	reader := bufio.NewReader(resp.Body)
	for {
		event, data, err := readSSEEvent(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return CallResponse{}, errors.New("sse stream closed before response")
			}
			return CallResponse{}, err
		}
		switch event {
		case "response":
			var rpcResp rpcResponse
			if err := json.Unmarshal(data, &rpcResp); err != nil {
				return CallResponse{}, err
			}
			if rpcResp.Error != nil {
				return CallResponse{}, rpcResp.Error.callerError()
			}
			return decodeToolCallResult(rpcResp.Result)
		case "error":
			var rpcResp rpcResponse
			if err := json.Unmarshal(data, &rpcResp); err != nil {
				return CallResponse{}, fmt.Errorf("mcp error event: %w", err)
			}
			if rpcResp.Error != nil {
				return CallResponse{}, rpcResp.Error.callerError()
			}
			return CallResponse{}, errors.New("mcp error event")
		case "", "notification":
			continue
		case "close":
			return CallResponse{}, errors.New("sse stream closed without response")
		default:
			continue
		}
	}
}

func readSSEEvent(reader *bufio.Reader) (string, []byte, error) {
	var event string
	var data []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if event == "" && len(data) == 0 {
				continue
			}
			return event, data, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if after, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(after)
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			chunk := after
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, chunk...)
			continue
		}
	}
}
