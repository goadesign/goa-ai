// InitializeSession sends the application identity, checks the selected
// protocol version and capabilities, and completes the MCP handshake.
func InitializeSession(ctx context.Context, client *Client, clientInfo mcpruntime.ClientInfo) error {
    if err := clientInfo.Validate(); err != nil {
        return err
    }
    if client == nil {
        panic("MCP session requires a generated client")
    }
    session, ok := client.Doer.(*mcpruntime.HTTPSession)
    if !ok {
        session = mcpruntime.NewHTTPSession(client.Doer, {{ .MCPPackage }}.DefaultProtocolVersion)
        client.Doer = session
    }
    session.Reset()
    if err := initializeSession(ctx, client, clientInfo, session); err != nil {
        session.Reset()
        return err
    }
    return nil
}

// initializeSession performs one complete handshake on a reset HTTP session.
func initializeSession(
    ctx context.Context,
    client *Client,
    clientInfo mcpruntime.ClientInfo,
    session *mcpruntime.HTTPSession,
) error {
    payload := &{{ .MCPPackage }}.InitializePayload{
        ProtocolVersion: {{ .MCPPackage }}.DefaultProtocolVersion,
        ClientInfo: &{{ .MCPPackage }}.ClientInfo{
            Name:    clientInfo.Name,
            Version: clientInfo.Version,
        },
        Capabilities: &{{ .MCPPackage }}.ClientCapabilities{},
    }
    ires, err := client.Initialize()(ctx, payload)
    if err != nil {
        return callerError(err)
    }
    result := ires.(*{{ .MCPPackage }}.InitializeResult)
    if result.ProtocolVersion != {{ .MCPPackage }}.DefaultProtocolVersion {
        return fmt.Errorf(
            "mcp: server selected protocol version %q, client supports %q",
            result.ProtocolVersion,
            {{ .MCPPackage }}.DefaultProtocolVersion,
        )
    }
    {{- if .HasTools }}
    if result.Capabilities.Tools == nil {
        return fmt.Errorf("mcp: server does not advertise tool support")
    }
    {{- end }}
    {{- if .HasResources }}
    if result.Capabilities.Resources == nil {
        return fmt.Errorf("mcp: server does not advertise resource support")
    }
    {{- end }}
    {{- if .HasPrompts }}
    if result.Capabilities.Prompts == nil {
        return fmt.Errorf("mcp: server does not advertise prompt support")
    }
    {{- end }}
    session.Begin()
    req, err := client.{{ .InitializedRequestBuilder }}(ctx, nil)
    if err != nil {
        return err
    }
    if err := {{ .InitializedRequestEncoder }}(client.encoder)(req, nil); err != nil {
        return err
    }
    resp, err := client.Doer.Do(req)
    if err != nil {
        return fmt.Errorf("send MCP initialized notification: %w", err)
    }
    body, readErr := io.ReadAll(resp.Body)
    closeErr := resp.Body.Close()
    if err := errors.Join(readErr, closeErr); err != nil {
        return fmt.Errorf("read MCP initialized notification response: %w", err)
    }
    if resp.StatusCode != http.StatusAccepted {
        return fmt.Errorf("MCP initialized notification returned HTTP status %d", resp.StatusCode)
    }
    if len(body) != 0 {
        return fmt.Errorf("MCP initialized notification returned a response body")
    }
    return nil
}

// callerError copies a JSON-RPC error code and message into the runtime error.
func callerError(err error) error {
    var rpcErr *{{ .JSONRPCPackage }}.ErrorResponse
    if errors.As(err, &rpcErr) {
        return &mcpruntime.Error{
            Code:    int(rpcErr.Code),
            Message: rpcErr.Message,
        }
    }
    return err
}
