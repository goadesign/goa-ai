{{ printf "%s registers the guarded MCP server at its authored HTTP paths." .Transport.MountServerDeclaration.Name | comment }}
func {{ .Transport.MountServerDeclaration.Name }}(mux goahttp.Muxer, h *{{ .Transport.ServerStructDeclaration.Name }}) {
    {{- range (index .Transport.Endpoints 0).Routes }}
    mux.Handle("{{ .Verb }}", "{{ .Path }}", h.ServeHTTP)
    mux.Handle("GET", "{{ .Path }}", h.ServeHTTP)
    mux.Handle("DELETE", "{{ .Path }}", h.ServeHTTP)
    {{- end }}
}

{{ printf "%s registers this guarded MCP server at its authored HTTP paths." .Transport.MountServerDeclaration.Name | comment }}
func (s *{{ .Transport.ServerStructDeclaration.Name }}) {{ .Transport.MountServerDeclaration.Name }}(mux goahttp.Muxer) {
    {{ .Transport.MountServerDeclaration.Name }}(mux, s)
}

// mcpResponseWriter records whether the JSON-RPC handler wrote a response.
type mcpResponseWriter struct {
	http.ResponseWriter
	written bool
}

// withMCPTransport enforces the HTTP rules that MCP adds to JSON-RPC.
func withMCPTransport(h *{{ .Transport.ServerStructDeclaration.Name }}, origins []string, next http.HandlerFunc) http.HandlerFunc {
    allowedOrigins := make(map[string]struct{}, len(origins))
    for _, origin := range origins {
        allowedOrigins[origin] = struct{}{}
    }
    bindings := map[string][]mcpruntime.HeaderBinding{
        {{- range .Tools }}
        {{- if .Headers }}
        {{ printf "%q" .Name }}: {
            {{- range .Headers }}
            {Name: {{ printf "%q" .Name }}, Type: {{ printf "%q" .Type }}, Path: []string{ {{ range .Path }}{{ printf "%q" . }}, {{ end }} }},
            {{- end }}
        },
        {{- end }}
        {{- end }}
    }
	return func(w http.ResponseWriter, r *http.Request) {
		if !mcpOriginAllowed(r, allowedOrigins) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

        if r.Method != http.MethodPost {
            w.WriteHeader(http.StatusMethodNotAllowed)
            return
        }

		originalBody := r.Body
		body, readErr := io.ReadAll(originalBody)
		closeErr := originalBody.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			h.errhandler(r.Context(), w, fmt.Errorf("read MCP request body: %w", err))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
        if failure := mcpruntime.ValidateHTTPRequest(r, body, bindings); failure != nil {
            if err := mcpruntime.WriteProtocolError(w, body, failure); err != nil {
                h.errhandler(r.Context(), w, err)
            }
            return
        }
        var request jsonrpc.RawRequest
        if err := request.UnmarshalJSON(body); err != nil {
            h.errhandler(r.Context(), w, err)
            return
        }
        if !request.HasID {
            w.WriteHeader(http.StatusAccepted)
            return
        }
        switch request.Method {
        {{- range .Transport.Endpoints }}
        case {{ printf "%q" .Method.Name }}:
        {{- end }}
        default:
            failure := &mcpruntime.Error{Code: mcpruntime.JSONRPCMethodNotFound, Message: "Method not found"}
            if err := mcpruntime.WriteProtocolError(w, body, failure); err != nil {
                h.errhandler(r.Context(), w, err)
            }
            return
        }

		response := &mcpResponseWriter{ResponseWriter: w}
        {{- if .ResourceSubscription }}
        if request.Method == "subscriptions/listen" {
            id, err := json.Marshal(request.ID)
            if err != nil {
                h.errhandler(r.Context(), response, err)
                return
            }
            if err := mcpruntime.ServeSubscriptions(response, r, id, request.Params, next); err != nil {
                var failure *mcpruntime.Error
                if !response.written && errors.As(err, &failure) {
                    if err := mcpruntime.WriteProtocolError(response, body, failure); err != nil {
                        h.errhandler(r.Context(), response, err)
                    }
                } else {
                    h.errhandler(r.Context(), response, err)
                }
            }
            return
        }
        {{- end }}
		if err := mcpruntime.ServeProgress(response, r, request.Params, next); err != nil {
			h.errhandler(r.Context(), response, err)
		}
		if !response.written {
			w.WriteHeader(http.StatusAccepted)
		}
	}
}

// mcpOriginAllowed reports whether the request omits Origin or names an origin
// the application allowed when it constructed the server.
func mcpOriginAllowed(r *http.Request, allowedOrigins map[string]struct{}) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	_, ok := allowedOrigins[origins[0]]
	return ok
}

// WriteHeader records that the JSON-RPC handler selected an HTTP status.
func (w *mcpResponseWriter) WriteHeader(statusCode int) {
	w.written = true
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write records that the JSON-RPC handler wrote a response body.
func (w *mcpResponseWriter) Write(data []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(data)
}

// Unwrap gives HTTP response control access to the underlying network writer.
func (w *mcpResponseWriter) Unwrap() http.ResponseWriter {
    return w.ResponseWriter
}
