package client

import (
    "context"
    "encoding/json"
    "errors"
    "io"

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
    return Caller{suite: suite, client: client}
}

// CallTool invokes tools/call via the generated JSON-RPC client and normalizes the response.
func (c Caller) CallTool(ctx context.Context, req mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
    if c.client == nil{

        return mcpruntime.CallResponse{

    }, errors.New("mcp client not configured")
    }
    payload := &mcppkg.ToolsCallPayload{Name: req.Tool, Arguments: json.RawMessage(req.Payload)}
    streamEndpoint := c.client.ToolsCall()
    stream, err := streamEndpoint(ctx, payload)
    if err != nil{

        return mcpruntime.CallResponse{

    }, err
    }
    clientStream, ok := stream.(*ToolsCallClientStream)
    if !ok{

        return mcpruntime.CallResponse{

    }, errors.New("invalid tools/call stream type")
    }
    var last *mcppkg.ToolsCallResult
    for {
        ev, recvErr := clientStream.Recv(ctx)
        if recvErr == io.EOF {
            break
        }
        if recvErr != nil{

            return mcpruntime.CallResponse{

        }, recvErr
        }
        last = ev
    }
    if last == nil || len(last.Content) == 0 {
        return mcpruntime.CallResponse{}, errors.New("empty MCP response")
    }
    
    return normalizeSDKToolResult(last)
}

func normalizeSDKToolResult(last *mcppkg.ToolsCallResult) (mcpruntime.CallResponse, error) {
    var textBytes []byte
    for _, item := range last.Content {
        if item.Text != nil {
            textBytes = append(textBytes, []byte(*item.Text)...)
        }
    }
    
    var result json.RawMessage
    if len(textBytes) > 0 {
        if json.Valid(textBytes) {
            result = append(json.RawMessage(nil), textBytes...)
        } else {
            // Store raw text wrapped in quotes (valid JSON string)
            marshaled, err := json.Marshal(string(textBytes))
            if err != nil {
                return mcpruntime.CallResponse{}, err
            }
            result = marshaled
        }
    } else {
        result = json.RawMessage("null")
    }

    var structured json.RawMessage
    if last.Content != nil {
        marshaledContent, err := json.Marshal(last.Content)
        if err == nil {
            structured = append(json.RawMessage(nil), marshaledContent...)
        }
    }

    return mcpruntime.CallResponse{Result: result, Structured: structured}, nil
}
