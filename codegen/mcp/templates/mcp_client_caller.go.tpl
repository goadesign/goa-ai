// Caller invokes typed tools through the generated stateless MCP client.
type Caller struct {
    client *Client
    transport *mcpruntime.HTTPTransport
    {{- range .Paths }}
    {{ .Name }} string
    {{- end }}
}

// NewCaller checks application identity and fixes the authored URL values for
// this caller. Each request keeps those values outside tool arguments; creating
// the caller does not contact the server.
func NewCaller(client *Client, info mcpruntime.ClientInfo, support mcpruntime.InputSupport, retry mcpruntime.HTTPRetryPolicy{{ range .Paths }}, {{ .Name }} string{{ end }}) (mcpruntime.Caller, error) {
    if err := retry.Validate(); err != nil { return nil, err }
    if err := info.Validate(); err != nil {
        return nil, err
    }
    transport := mcpruntime.NewHTTPTransport(client.Doer, info, mcpHTTPBindings(), support, retry)

    return &Caller{client: client, transport: transport{{ range .Paths }}, {{ .Name }}: {{ .Name }}{{ end }}}, nil
}

// CallTool sends exact arguments and returns validated content beside domain JSON.
func (c *Caller) CallTool(ctx context.Context, req mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
    payload := &{{ .PayloadRef }}{Name: req.Tool, Arguments: json.RawMessage(req.Payload){{ range .Paths }}, {{ .Selector }}: c.{{ .Name }}{{ end }}}
    request, err := c.client.BuildToolsCallRequest(ctx, payload)
    if err != nil { return mcpruntime.CallResponse{}, err }
    return c.transport.CallTool(ctx, request.URL.String(), req)
}
