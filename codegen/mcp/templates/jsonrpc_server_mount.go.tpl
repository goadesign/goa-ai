{{ printf "%s configures the mux to serve the JSON-RPC %s service methods." .Transport.MountServerDeclaration.Name .Transport.Service.Name | comment }}
func {{ .Transport.MountServerDeclaration.Name }}(mux goahttp.Muxer, h *{{ .Transport.ServerStructDeclaration.Name }}) {
	MountWithOrigins(mux, h, nil)
}

// MountWithOrigins configures the mux to serve the JSON-RPC service. Requests
// that send an Origin header must exactly match one of the allowed origins.
func MountWithOrigins(mux goahttp.Muxer, h *{{ .Transport.ServerStructDeclaration.Name }}, origins []string) {
	allowedOrigins := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowedOrigins[origin] = struct{}{}
	}
{{- if .Transport.HasMixed }}
	// ServeHTTP checks the Accept header and chooses one response or a stream of events.
	{{- range (index .Transport.Endpoints 0).Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPTransport(h, allowedOrigins, h.ServeHTTP))
	{{- end }}
{{- else if .Transport.HasSSE }}
	// Every method in this server writes a stream of events.
	{{- range .Transport.Endpoints }}
		{{- range .Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPTransport(h, allowedOrigins, h.handleSSE))
		{{- end }}
	{{- end }}
{{- else }}
	// Every method in this server writes one JSON-RPC response.
	{{- range (index .Transport.Endpoints 0).Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPTransport(h, allowedOrigins, h.ServeHTTP))
	{{- end }}
{{- end }}
	{{- range (index .Transport.Endpoints 0).Routes }}
	mux.Handle("GET", "{{ .Path }}", mcpMethodNotAllowed(allowedOrigins))
	mux.Handle("DELETE", "{{ .Path }}", mcpMethodNotAllowed(allowedOrigins))
	{{- end }}
}

{{ printf "%s configures the mux to serve the JSON-RPC %s service methods." .Transport.MountServerDeclaration.Name .Transport.Service.Name | comment }}
func (s *{{ .Transport.ServerStructDeclaration.Name }}) {{ .Transport.MountServerDeclaration.Name }}(mux goahttp.Muxer) {
	{{ .Transport.MountServerDeclaration.Name }}(mux, s)
}

// MountWithOrigins configures the mux to serve this JSON-RPC service.
// Requests that send an Origin header must exactly match an allowed origin.
func (s *{{ .Transport.ServerStructDeclaration.Name }}) MountWithOrigins(mux goahttp.Muxer, origins []string) {
	MountWithOrigins(mux, s, origins)
}

// mcpResponseWriter records whether the JSON-RPC handler wrote a response.
type mcpResponseWriter struct {
	http.ResponseWriter
	written bool
}

// withMCPTransport enforces the HTTP rules that MCP adds to JSON-RPC.
func withMCPTransport(h *{{ .Transport.ServerStructDeclaration.Name }}, allowedOrigins map[string]struct{}, next http.HandlerFunc) http.HandlerFunc {
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
		next(response, r)
		if !response.written {
			w.WriteHeader(http.StatusAccepted)
		}
	}
}

// mcpMethodNotAllowed rejects HTTP methods absent from the current MCP binding.
func mcpMethodNotAllowed(allowedOrigins map[string]struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !mcpOriginAllowed(r, allowedOrigins) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// mcpOriginAllowed reports whether the request omits Origin or names an origin
// the application allowed when it mounted the server.
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
