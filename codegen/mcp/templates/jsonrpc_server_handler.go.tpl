// handleHTTP decodes one MCP request and dispatches its generated protocol method.
// ServeHTTP validates metadata and headers before configured middleware calls it.
func (s *{{ .ServerStructDeclaration.Name }}) handleHTTP(w http.ResponseWriter, r *http.Request) {
    var request jsonrpc.RawRequest
    if err := s.decoder(r).Decode(&request); err != nil {
        s.encodeJSONRPCError(r.Context(), w, &request, jsonrpc.ParseError, "Parse error", nil)
        return
    }
    switch request.Method {
    {{- range .Endpoints }}
    case {{ printf "%q" .Method.Name }}:
        if err := s.{{ .Method.VarName }}(r.Context(), r, &request, w); err != nil {
            s.errhandler(r.Context(), w, fmt.Errorf("MCP handler %s: %w", {{ printf "%q" .Method.Name }}, err))
        }
    {{- end }}
    default:
        s.encodeJSONRPCError(r.Context(), w, &request, jsonrpc.MethodNotFound, "Method not found", nil)
    }
}
