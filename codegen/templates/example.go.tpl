// stringPtr returns a pointer to the given string
func stringPtr(s string) *string {
	return &s
}

// simpleLogger implements middleware.Logger
type simpleLogger struct {
	debug bool
}

func (l *simpleLogger) Log(keyvals ...any) error {
	if len(keyvals) == 0 {
		return nil
	}
	prefix := "[MCP]"
	if l.debug {
		prefix = "[MCP DEBUG]"
	}
	fmt.Fprintf(os.Stderr, "%s %s ", prefix, time.Now().Format("15:04:05"))
	for i := 0; i < len(keyvals); i += 2 {
		if i+1 < len(keyvals) {
			fmt.Fprintf(os.Stderr, "%v=%v ", keyvals[i], keyvals[i+1])
		}
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

{{ if .HasPrompts -}}
// Example{{ goify .ServiceName }}PromptProvider implements the MCP prompt provider interface
type Example{{ goify .ServiceName }}PromptProvider struct{}
{{- end }}

{{- range .StaticPrompts }}
// Get{{ goify .Name }}Prompt implements the prompt provider interface
func (p *Example{{ goify $.ServiceName }}PromptProvider) Get{{ goify .Name }}Prompt(arguments json.RawMessage) (*{{ $.Package }}.PromptsGetResult, error) {
	desc := "{{ .Description }}"
	return &{{ $.Package }}.PromptsGetResult{
		Description: &desc,
		Messages: []*{{ $.Package }}.PromptMessage{
			{{- range .Messages }}
			{
				Role:    "{{ .Role }}",
				Content: &{{ $.Package }}.MessageContent{
					Type: "text",
					Text: stringPtr("{{ .Content }}"),
				},
			},
			{{- end }}
		},
	}, nil
}
{{- end }}

{{- range .DynamicPrompts }}
// Get{{ goify .Name }}Prompt implements the dynamic prompt provider interface
func (p *Example{{ goify $.ServiceName }}PromptProvider) Get{{ goify .Name }}Prompt(arguments json.RawMessage) (*{{ $.Package }}.PromptsGetResult, error) {
	// Parse arguments if needed
	desc := "{{ .Description }}"
	return &{{ $.Package }}.PromptsGetResult{
		Description: &desc,
		Messages: []*{{ $.Package }}.PromptMessage{
			{
				Role:    "system",
				Content: &{{ $.Package }}.MessageContent{
					Type: "text",
					Text: stringPtr("Dynamic prompt for {{ .Name }}"),
				},
			},
		},
	}, nil
}
{{- end }}

func main() {
	var (
		hostF = flag.String("host", "localhost", "Server host")
		portF = flag.String("port", "8080", "Server port")
		dbgF  = flag.Bool("debug", false, "Enable debug logging")
	)
	flag.Parse()

	// Setup logger
	logger := &simpleLogger{
		debug: *dbgF,
	}

	// Create the original service
	svc := {{ .Package }}api.New{{ goify .ServiceName }}()

	{{- if .HasPrompts }}
	// Create the prompt provider
	promptProvider := &Example{{ goify .ServiceName }}PromptProvider{}
	
	// Wire up the MCP handler
	handler := integration.WireMCP(svc, promptProvider, logger)
	{{- else }}
	// Wire up the MCP handler (no prompts)
	handler := integration.WireMCP(svc, logger)
	{{- end }}

	// Create HTTP server
	addr := fmt.Sprintf("%s:%s", *hostF, *portF)
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	// Handle shutdown gracefully
	idleConnsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint

		// Shutdown server
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Log("msg", "HTTP server Shutdown error", "err", err)
		}
		close(idleConnsClosed)
	}()

	// Start server
	logger.Log("msg", "MCP server listening", "addr", addr)
	logger.Log("msg", "MCP endpoint", "path", "/mcp/mcp")
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Log("msg", "HTTP server ListenAndServe error", "err", err)
		os.Exit(1)
	}

	<-idleConnsClosed
	logger.Log("msg", "Server stopped")
}