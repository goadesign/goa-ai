// NewMCPHandler creates an HTTP handler for the MCP protocol service
// that delegates to the original {{ .ServiceName }} service
func NewMCPHandler(
	mux goahttp.Muxer,
	enc func(context.Context, http.ResponseWriter) goahttp.Encoder,
	dec func(*http.Request) goahttp.Decoder,
	eh func(context.Context, http.ResponseWriter, error),
	originalService {{ .Package }}svc.Service,
{{- if .HasPrompts }}
	promptProvider {{ .Package }}.PromptProvider,
{{- end }}
) http.Handler {
	// Create the MCP adapter that wraps the original service
{{- if .HasPrompts }}
	adapter := {{ .Package }}.NewMCPAdapter(originalService, promptProvider)
{{- else }}
	adapter := {{ .Package }}.NewMCPAdapter(originalService)
{{- end }}
	
	// Create endpoints for the MCP service (adapter implements the MCP service interface)
	endpoints := {{ .Package }}.NewEndpoints(adapter)
	
	// Create the JSON-RPC server for the MCP service
	s := {{ .Package }}svr.New(endpoints, mux, dec, enc, eh)
	
	// Mount the MCP handlers
	{{ .Package }}svr.Mount(mux, s)
	
	return mux
}

// WireMCP wires the MCP protocol service with the original service
// This is a convenience function for setting up the MCP server in main.go
func WireMCP(
	originalService {{ .Package }}svc.Service,
{{- if .HasPrompts }}
	promptProvider {{ .Package }}.PromptProvider,
{{- end }}
	logger middleware.Logger,
) http.Handler {
	// Create HTTP muxer
	mux := goahttp.NewMuxer()
	
	// Create encoder/decoder functions
	dec := func(r *http.Request) goahttp.Decoder {
		return goahttp.RequestDecoder(r)
	}
	enc := func(ctx context.Context, w http.ResponseWriter) goahttp.Encoder {
		return goahttp.ResponseEncoder(ctx, w)
	}
	
	// Create error handler
	eh := func(ctx context.Context, w http.ResponseWriter, err error) {
		logger.Log("msg", "JSON-RPC error", "err", err)
		if err := enc(ctx, w).Encode(err); err != nil {
			logger.Log("msg", "failed to encode error", "err", err)
		}
	}
	
	// Create and return the handler
{{- if .HasPrompts }}
	return NewMCPHandler(mux, enc, dec, eh, originalService, promptProvider)
{{- else }}
	return NewMCPHandler(mux, enc, dec, eh, originalService)
{{- end }}
}