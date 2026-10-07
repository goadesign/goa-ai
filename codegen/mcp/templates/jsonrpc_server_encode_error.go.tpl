// encodeJSONRPCError preserves the request identifier and writes a protocol
// failure with its HTTP status. It never wraps protocol data as a service result.
func (s *{{ .ServerStructDeclaration.Name }}) encodeJSONRPCError(ctx context.Context, w http.ResponseWriter, req *jsonrpc.RawRequest, code jsonrpc.Code, message string, data any) {
    {{ .EncodeError.Name }}(ctx, w, req, code, message, data, s.encoder, s.errhandler)
}

// {{ .EncodeError.Name }} writes the JSON-RPC error with the HTTP binding's status.
func {{ .EncodeError.Name }}(ctx context.Context, w http.ResponseWriter, req *jsonrpc.RawRequest, code jsonrpc.Code, message string, data any, encoder func(context.Context, http.ResponseWriter) goahttp.Encoder, errhandler func(context.Context, http.ResponseWriter, error)) {
    status := http.StatusBadRequest
    if code == jsonrpc.MethodNotFound {
        status = http.StatusNotFound
    }
    if code == jsonrpc.InternalError {
        status = http.StatusInternalServerError
    }
    if code == jsonrpc.Code(mcpruntime.MissingRequiredClientCapability) {
        // This unique MCP error code carries its typed data directly. Goa's
        // designed-error decoder removes only the framework's name wrapper.
        encoded, err := json.Marshal(data)
        if err != nil {
            errhandler(ctx, w, fmt.Errorf("encode required client capabilities: %w", err))
            return
        }
        name, body, ok := jsonrpc.DecodeServiceErrorData(encoded)
        if !ok || name != "missing_client_capability" {
            panic("MCP capability error does not match its designed Goa data")
        }
        data = body
    }
    response := jsonrpc.MakeErrorResponse(req.ID, code, message, data)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if err := encoder(ctx, w).Encode(response); err != nil {
        errhandler(ctx, w, fmt.Errorf("encode MCP protocol error: %w", err))
    }
}
