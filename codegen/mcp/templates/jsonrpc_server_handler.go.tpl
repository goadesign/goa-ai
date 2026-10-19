// handleHTTP decodes one MCP request and dispatches its generated protocol method.
// ServeHTTP validates authentication and protocol rules first. Application
// middleware then chooses response headers before this handler starts streaming.
func (s *{{ .Transport.ServerStructDeclaration.Name }}) handleHTTP(w http.ResponseWriter, r *http.Request) {
    var request jsonrpc.RawRequest
    if err := s.decoder(r).Decode(&request); err != nil {
        s.encodeJSONRPCError(r.Context(), w, &request, jsonrpc.ParseError, "Parse error", nil)
        return
    }
    dispatch := func(w http.ResponseWriter, r *http.Request) {
    switch request.Method {
    case "initialize":
        request.Method = "server/discover"
        {{- range .Transport.Endpoints }}
        {{- if eq .Method.Name "server/discover" }}
        if err := s.{{ .Method.VarName }}(r.Context(), r, &request, w); err != nil {
            s.errhandler(r.Context(), w, fmt.Errorf("MCP initialize: %w", err))
        }
        {{- end }}
        {{- end }}
    case "ping":
        if err := s.encoder(r.Context(), w).Encode(jsonrpc.MakeSuccessResponse(request.ID, struct{}{})); err != nil {
            s.errhandler(r.Context(), w, fmt.Errorf("MCP ping: %w", err))
        }
    {{- range .Transport.Endpoints }}
    case {{ printf "%q" .Method.Name }}:
        if err := s.{{ .Method.VarName }}(r.Context(), r, &request, w); err != nil {
            s.errhandler(r.Context(), w, fmt.Errorf("MCP handler %s: %w", {{ printf "%q" .Method.Name }}, err))
        }
    {{- end }}
    default:
        s.encodeJSONRPCError(r.Context(), w, &request, jsonrpc.MethodNotFound, "Method not found", nil)
    }
    }
        {{- if .SubscriptionSource }}
        if request.Method == "subscriptions/listen" {
            response := &mcpResponseWriter{ResponseWriter: w}
            id, err := json.Marshal(request.ID)
            if err != nil {
                s.errhandler(r.Context(), response, err)
                return
            }
            if err := mcpruntime.ServeSubscriptions(response, r, id, request.Params, dispatch); err != nil {
                var failure *mcpruntime.Error
                if !response.written && errors.As(err, &failure) {
                    encoded, encodeErr := json.Marshal(jsonrpc.Request{JSONRPC: request.JSONRPC, Method: request.Method, Params: request.Params, ID: request.ID})
                    if encodeErr != nil {
                        s.errhandler(r.Context(), response, encodeErr)
                        return
                    }
                    if err := mcpruntime.WriteProtocolError(response, encoded, failure); err != nil {
                        s.errhandler(r.Context(), response, err)
                    }
                } else {
                    s.errhandler(r.Context(), response, err)
                }
            }
            return
        }
        {{- end }}
		if err := mcpruntime.ServeProgress(w, r, request.Params, dispatch); err != nil {
			s.errhandler(r.Context(), w, err)
		}
}
