{{ printf "%s configures the mux to serve the JSON-RPC %s service methods." .MountServerDeclaration.Name .Service.Name | comment }}
func {{ .MountServerDeclaration.Name }}(mux goahttp.Muxer, h *{{ .ServerStructDeclaration.Name }}) {
	MountWithOrigins(mux, h, nil)
}

// MountWithOrigins configures the mux to serve the JSON-RPC service. Requests
// that send an Origin header must exactly match one of the allowed origins.
func MountWithOrigins(mux goahttp.Muxer, h *{{ .ServerStructDeclaration.Name }}, origins []string) {
	allowedOrigins := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowedOrigins[origin] = struct{}{}
	}
{{- if .HasMixed }}
	// ServeHTTP checks the Accept header and chooses one response or a stream of events.
	{{- range (index .Endpoints 0).Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPTransport(h, allowedOrigins, h.ServeHTTP))
	{{- end }}
{{- else if .HasSSE }}
	// Every method in this server writes a stream of events.
	{{- range .Endpoints }}
		{{- range .Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPTransport(h, allowedOrigins, h.handleSSE))
		{{- end }}
	{{- end }}
{{- else }}
	// Every method in this server writes one JSON-RPC response.
	{{- range (index .Endpoints 0).Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPTransport(h, allowedOrigins, h.ServeHTTP))
	{{- end }}
{{- end }}
	{{- range (index .Endpoints 0).Routes }}
	mux.Handle("GET", "{{ .Path }}", mcpGETHandler(allowedOrigins))
	{{- end }}
}

{{ printf "%s configures the mux to serve the JSON-RPC %s service methods." .MountServerDeclaration.Name .Service.Name | comment }}
func (s *{{ .ServerStructDeclaration.Name }}) {{ .MountServerDeclaration.Name }}(mux goahttp.Muxer) {
	{{ .MountServerDeclaration.Name }}(mux, s)
}

// MountWithOrigins configures the mux to serve this JSON-RPC service.
// Requests that send an Origin header must exactly match an allowed origin.
func (s *{{ .ServerStructDeclaration.Name }}) MountWithOrigins(mux goahttp.Muxer, origins []string) {
	MountWithOrigins(mux, s, origins)
}

// mcpResponseWriter records whether the JSON-RPC handler wrote a response.
type mcpResponseWriter struct {
	http.ResponseWriter
	written bool
}

// withMCPTransport enforces the HTTP rules that MCP adds to JSON-RPC.
func withMCPTransport(h *{{ .ServerStructDeclaration.Name }}, allowedOrigins map[string]struct{}, next http.HandlerFunc) http.HandlerFunc {
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
		body = bytes.TrimSpace(body)
		if len(body) > 0 && body[0] == '[' {
			response := jsonrpc.MakeErrorResponse(nil, jsonrpc.InvalidRequest, "Invalid request", nil)
			if err := h.encoder(r.Context(), w).Encode(response); err != nil {
				h.errhandler(r.Context(), w, fmt.Errorf("encode MCP invalid request response: %w", err))
			}
			return
		}
		var request jsonrpc.RawRequest
		if err := request.UnmarshalJSON(body); err == nil && request.HasMethod && request.Method != "initialize" &&
			r.Header.Get("MCP-Protocol-Version") != {{ .Service.PkgName }}.DefaultProtocolVersion {
			http.Error(w, "Unsupported MCP protocol version", http.StatusBadRequest)
			return
		}

		response := &mcpResponseWriter{ResponseWriter: w}
		next(response, r)
		if !response.written {
			w.WriteHeader(http.StatusAccepted)
		}
	}
}

// mcpGETHandler rejects server event streams because this generated server
// supports request responses only.
func mcpGETHandler(allowedOrigins map[string]struct{}) http.HandlerFunc {
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
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	_, ok := allowedOrigins[origin]
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
