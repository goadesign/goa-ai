package client

import (
    "context"
    "encoding/json"
    "errors"
    "io"

    genjsonrpc "goa.design/goa/v3/jsonrpc"
    mcpruntime "goa.design/goa-ai/runtime/mcp"
    mcppkg "{{ .MCPImportPath }}"
)

// Caller adapts the generated MCP JSON-RPC client to the runtime Caller interface.
type Caller struct {
    suite string
    client *Client
}

// NewCaller wraps the generated Client so it can register with the goa-ai runtime.
func NewCaller(client *Client, suite string) mcpruntime.Caller {
    if client == nil {
        panic("MCP caller requires a generated client")
    }
    return Caller{suite: suite, client: client}
}

// CallTool invokes tools/call via the generated JSON-RPC client and normalizes the response.
func (c Caller) CallTool(ctx context.Context, req mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
    payload := &mcppkg.ToolsCallPayload{Name: req.Tool, Arguments: json.RawMessage(req.Payload)}
    streamEndpoint := c.client.ToolsCall()
    stream, err := streamEndpoint(ctx, payload)
    if err != nil{

        return mcpruntime.CallResponse{

    }, callerError(err)
    }
    clientStream, ok := stream.(*ToolsCallClientStream)
    if !ok{

        return mcpruntime.CallResponse{

    }, mcpruntime.NewInternalError(errors.New("invalid tools/call stream type"))
    }
    var last *mcppkg.ToolsCallResult
    for {
        ev, recvErr := clientStream.Recv(ctx)
        if recvErr == io.EOF {
            break
        }
        if recvErr != nil{

            return mcpruntime.CallResponse{

        }, callerError(recvErr)
        }
        last = ev
    }
    if last == nil || len(last.Content) == 0{

        return mcpruntime.CallResponse{

    }, mcpruntime.NewMalformedResponseError(errors.New("empty MCP response"))
    }
    item := last.Content[0]
    var result json.RawMessage
    if item.Text != nil {
        txt := []byte(*item.Text)
        if json.Valid(txt) {
            result = append(json.RawMessage(nil), txt...)
        } else {
            marshaled, err := json.Marshal(*item.Text)
            if err != nil{

                return mcpruntime.CallResponse{

            }, err
            }
            result = marshaled
        }
    } else {
        result = json.RawMessage("null")
    }
    if last.IsError != nil && *last.IsError {
        return mcpruntime.CallResponse{}, mcpruntime.NewToolExecutionError(result)
    }
    var structured json.RawMessage
    if item.MimeType != nil && *item.MimeType == "application/json" {
        structured = append(json.RawMessage(nil), result...)
    }
    return mcpruntime.CallResponse{Result: result, Structured: structured}, nil
}

// callerError preserves JSON-RPC error codes across the generated transport
// boundary so agent recovery can classify protocol failures structurally.
func callerError(err error) error {
    var rpcErr *genjsonrpc.ErrorResponse
    if errors.As(err, &rpcErr) {
        return &mcpruntime.Error{
            Code:    int(rpcErr.Code),
            Message: rpcErr.Message,
        }
    }
    return err
}
