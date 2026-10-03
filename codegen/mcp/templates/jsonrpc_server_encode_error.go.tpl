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
    response := jsonrpc.MakeErrorResponse(req.ID, code, message, data)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if err := encoder(ctx, w).Encode(response); err != nil {
        errhandler(ctx, w, fmt.Errorf("encode MCP protocol error: %w", err))
    }
}
