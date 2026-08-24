{{ printf "%s configures the mux to serve the JSON-RPC %s service methods." .MountServer .Service.Name | comment }}
func {{ .MountServer }}(mux goahttp.Muxer, h *{{ .ServerStruct }}) {
{{- if .HasMixed }}
	// Mixed transports: mount unified handler that negotiates HTTP vs SSE by Accept header.
	//
	// MCP policy headers are propagated via request context so the service
	// implementation can enforce per-request allow/deny lists.
	{{- range (index .Endpoints 0).Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPPolicyHeaders(h.ServeHTTP))
	{{- end }}
{{- else if .HasSSE }}
	// SSE only: mount SSE handler and propagate MCP policy headers via context.
	{{- range .Endpoints }}
		{{- range .Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPPolicyHeaders(h.handleSSE))
		{{- end }}
	{{- end }}
{{- else }}
	// HTTP only: propagate MCP policy headers via context.
	{{- range (index .Endpoints 0).Routes }}
	mux.Handle("{{ .Verb }}", "{{ .Path }}", withMCPPolicyHeaders(h.ServeHTTP))
	{{- end }}
{{- end }}
}

{{ printf "%s configures the mux to serve the JSON-RPC %s service methods." .MountServer .Service.Name | comment }}
func (s *{{ .ServerStruct }}) {{ .MountServer }}(mux goahttp.Muxer) {
	{{ .MountServer }}(mux, s)
}

// withMCPPolicyHeaders propagates MCP policy header values into the request context.
//
// The MCP adapter enforces resource allow/deny policies based on context values:
//   - "mcp_allow_names" (CSV list of resource names)
//   - "mcp_deny_names"  (CSV list of resource names)
//
// This helper maps those values from the corresponding HTTP headers:
//   - x-mcp-allow-names
//   - x-mcp-deny-names
//
// It is installed by the JSON-RPC Mount functions so consumers do not need
// to patch example servers or wire middleware manually.
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


