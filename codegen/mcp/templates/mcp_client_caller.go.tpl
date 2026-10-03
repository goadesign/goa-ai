// Caller invokes typed tools through the generated stateless MCP client.
type Caller struct {
    client *Client
    transport *mcpruntime.HTTPTransport
}

// NewCaller checks application identity and binds it to each request. It does
// not contact the server or require a discovery request before invoking a tool.
func NewCaller(client *Client, info mcpruntime.ClientInfo, support mcpruntime.InputSupport, retry mcpruntime.HTTPRetryPolicy) (mcpruntime.Caller, error) {
    if err := retry.Validate(); err != nil { return nil, err }
    if err := info.Validate(); err != nil {
        return nil, err
    }
    transport := mcpruntime.NewHTTPTransport(client.Doer, info, map[string]mcpruntime.ToolBinding{
        {{- range .Tools }}
        {{- if or .Headers .ReadOnly .Idempotent }}
        {{ printf "%q" .Name }}: {
            {{- if .ReadOnly }}ReadOnly: true,{{ end }}
            {{- if .Idempotent }}Idempotent: true,{{ end }}
            {{- if .Headers }}
            Headers: []mcpruntime.HeaderBinding{
            {{- range .Headers }}
            {Name: {{ printf "%q" .Name }}, Type: {{ printf "%q" .Type }}, Path: []string{ {{ range .Path }}{{ printf "%q" . }}, {{ end }} }},
            {{- end }}
            },
            {{- end }}
        },
        {{- end }}
        {{- end }}
    }, support, retry)
    return &Caller{client: client, transport: transport}, nil
}

// CallTool sends the tool's exact arguments and returns its structured content.
func (c *Caller) CallTool(ctx context.Context, req mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
    payload := &{{ .MCPPackage }}.ToolsCallPayload{Name: req.Tool, Arguments: json.RawMessage(req.Payload)}
    request, err := c.client.BuildToolsCallRequest(ctx, payload)
    if err != nil { return mcpruntime.CallResponse{}, err }
    return c.transport.CallTool(ctx, request.URL.String(), req)
}
