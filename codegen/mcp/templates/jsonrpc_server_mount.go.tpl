{{ printf "%s configures the mux to serve the JSON-RPC %s service methods." .MountServerDeclaration.Name .Service.Name | comment }}
func {{ .MountServerDeclaration.Name }}(mux goahttp.Muxer, h *{{ .ServerStructDeclaration.Name }}) {
{{- if .HasMixed }}
	// ServeHTTP checks the Accept header and chooses one response or a stream of events.
	{{- range (index .Endpoints 0).Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPPolicyHeaders(h.ServeHTTP))
	{{- end }}
{{- else if .HasSSE }}
	// Every method in this server writes a stream of events.
	{{- range .Endpoints }}
		{{- range .Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPPolicyHeaders(h.handleSSE))
		{{- end }}
	{{- end }}
{{- else }}
	// Every method in this server writes one JSON-RPC response.
	{{- range (index .Endpoints 0).Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPPolicyHeaders(h.ServeHTTP))
	{{- end }}
{{- end }}
}

{{ printf "%s configures the mux to serve the JSON-RPC %s service methods." .MountServerDeclaration.Name .Service.Name | comment }}
func (s *{{ .ServerStructDeclaration.Name }}) {{ .MountServerDeclaration.Name }}(mux goahttp.Muxer) {
	{{ .MountServerDeclaration.Name }}(mux, s)
}

// withMCPPolicyHeaders makes the request's allow and deny headers available to the MCP service.
func withMCPPolicyHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if allow := r.Header.Get("x-mcp-allow-names"); allow != "" {
			ctx = context.WithValue(ctx, "mcp_allow_names", allow)
		}
		if deny := r.Header.Get("x-mcp-deny-names"); deny != "" {
			ctx = context.WithValue(ctx, "mcp_deny_names", deny)
		}
		next(w, r.WithContext(ctx))
	}
}

