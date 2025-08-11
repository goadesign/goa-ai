// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}

// Helper function to create bool pointer
func boolPtr(b bool) *bool {
	return &b
}

// Helper function to generate unique IDs
func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// MCPAdapter implements the MCP protocol service by delegating to the original {{ .ServiceName }} service
type MCPAdapter struct {
	service {{ .Package }}.Service
	// Cache for static data
	toolsCache     []*{{ .Package }}.ToolInfo
	resourcesCache []*{{ .Package }}.ResourceInfo
	promptsCache   []*{{ .Package }}.PromptInfo
{{- if or .StaticPrompts .DynamicPrompts }}
	// Prompt provider for handling prompt requests
	promptProvider PromptProvider
{{- end }}
}

// NewMCPAdapter creates a new MCP adapter for the {{ .ServiceName }} service
{{- if or .StaticPrompts .DynamicPrompts }}
func NewMCPAdapter(svc {{ .Package }}.Service, promptProvider PromptProvider) *MCPAdapter {
{{- else }}
func NewMCPAdapter(svc {{ .Package }}.Service) *MCPAdapter {
{{- end }}
	adapter := &MCPAdapter{
		service: svc,
{{- if or .StaticPrompts .DynamicPrompts }}
		promptProvider: promptProvider,
{{- end }}
	}
	
	// Initialize caches
	adapter.initializeToolsCache()
	adapter.initializeResourcesCache()
	adapter.initializePromptsCache()
	
	return adapter
}

// Initialize establishes the MCP session
func (a *MCPAdapter) Initialize(ctx context.Context, p *{{ .Package }}.InitializePayload) (*{{ .Package }}.InitializeResult, error) {
	result := &{{ .Package }}.InitializeResult{
		ProtocolVersion: "2025-06-18",
		ServerInfo: &{{ .Package }}.ServerInfo{
			Name:    "{{ .MCPName }}",
			Version: "{{ .MCPVersion }}",
		},
		Capabilities: &{{ .Package }}.Capabilities{},
	}

{{- if .Tools }}
	// Tools capability
	result.Capabilities.Tools = map[string]any{
		"enabled": true,
	}
{{- end }}

{{- if .Resources }}
	// Resources capability
	result.Capabilities.Resources = map[string]any{
		"enabled": true,
	}
{{- end }}

{{- if or .StaticPrompts .DynamicPrompts }}
	// Prompts capability
	result.Capabilities.Prompts = map[string]any{
		"enabled": true,
	}
{{- end }}

	return result, nil
}

// Ping checks server availability
func (a *MCPAdapter) Ping(ctx context.Context) (*{{ .Package }}.PingResult, error) {
	return &{{ .Package }}.PingResult{
		Pong: true,
	}, nil
}

{{- if .Tools }}

// ToolsList returns the list of available tools
func (a *MCPAdapter) ToolsList(ctx context.Context, p *{{ .Package }}.ToolsListPayload) (*{{ .Package }}.ToolsListResult, error) {
	return &{{ .Package }}.ToolsListResult{
		Tools: a.toolsCache,
	}, nil
}

// ToolsCall executes a tool
func (a *MCPAdapter) ToolsCall(ctx context.Context, p *{{ .Package }}.ToolsCallPayload) (*{{ .Package }}.ToolsCallResult, error) {
	switch p.Name {
	{{- range .Tools }}
	case "{{ .Name }}":
		{{- if .HasPayload }}
		// Unmarshal MCP arguments (json.RawMessage) directly to service payload
		var payload {{ .PayloadType }}
		if err := json.Unmarshal(p.Arguments, &payload); err != nil {
			return &{{ $.Package }}.ToolsCallResult{
				Content: []*{{ $.Package }}.ContentItem{
					{Type: "text", Text: stringPtr(fmt.Sprintf("Invalid arguments for {{ .Name }}: %v", err))},
				},
				IsError: boolPtr(true),
			}, nil
		}

		// Call the service method
		{{- if .HasResult }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- else }}
		err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- end }}
		{{- else }}
		// Call the service method (no payload)
		{{- if .HasResult }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- else }}
		err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		{{- end }}
		
		if err != nil {
			return &{{ $.Package }}.ToolsCallResult{
				Content: []*{{ $.Package }}.ContentItem{
					{Type: "text", Text: stringPtr(fmt.Sprintf("Tool {{ .Name }} failed: %v", err))},
				},
				IsError: boolPtr(true),
			}, nil
		}

		{{- if .HasResult }}
		// Convert result to JSON for MCP response
		resultData, err := json.Marshal(result)
		if err != nil {
			return &{{ $.Package }}.ToolsCallResult{
				Content: []*{{ $.Package }}.ContentItem{
					{Type: "text", Text: stringPtr(fmt.Sprintf("Failed to marshal result: %v", err))},
				},
				IsError: boolPtr(true),
			}, nil
		}

		return &{{ $.Package }}.ToolsCallResult{
			Content: []*{{ $.Package }}.ContentItem{
				{Type: "text", Text: stringPtr(string(resultData))},
			},
		}, nil
		{{- else }}
		// No result, return success
		return &{{ $.Package }}.ToolsCallResult{
			Content: []*{{ $.Package }}.ContentItem{
				{Type: "text", Text: "Tool executed successfully"},
			},
		}, nil
		{{- end }}
	{{- end }}
	default:
		return &{{ .Package }}.ToolsCallResult{
			Content: []*{{ .Package }}.ContentItem{
				{
					Type: "text",
					Text: stringPtr(fmt.Sprintf("Unknown tool: %s", p.Name)),
				},
			},
			IsError: boolPtr(true),
		}, nil
	}
}


// initializeToolsCache builds the static tools list
func (a *MCPAdapter) initializeToolsCache() {
	a.toolsCache = []*{{ .Package }}.ToolInfo{
		{{- range .Tools }}
		{
			Name:        "{{ .Name }}",
			Description: stringPtr("{{ .Description }}"),
			InputSchema: map[string]any{
				"type": "object",
			},
		},
		{{- end }}
	}
}
{{- end }}

{{- if .Resources }}

// ResourcesList returns the list of available resources
func (a *MCPAdapter) ResourcesList(ctx context.Context, p *{{ .Package }}.ResourcesListPayload) (*{{ .Package }}.ResourcesListResult, error) {
	return &{{ .Package }}.ResourcesListResult{
		Resources: a.resourcesCache,
	}, nil
}

// ResourcesRead reads a resource
func (a *MCPAdapter) ResourcesRead(ctx context.Context, p *{{ .Package }}.ResourcesReadPayload) (*{{ .Package }}.ResourcesReadResult, error) {
	{{- range .Resources }}
	if p.URI == "{{ .URI }}" {
		return a.read{{ goify .Name }}(ctx)
	}
	{{- end }}

	return nil, fmt.Errorf("unknown resource URI: %s", p.URI)
}

{{- range .Resources }}
// read{{ goify .Name }} handles reading the {{ .Name }} resource
func (a *MCPAdapter) read{{ goify .Name }}(ctx context.Context) (*{{ $.Package }}.ResourcesReadResult, error) {
	// Call the original service method
	{{- if .HasResult }}
	{{- if .HasPayload }}
	// Create payload for methods that require it
	var payload {{ .PayloadType }}
	// TODO: Initialize payload fields as needed
	result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
	{{- else }}
	result, err := a.service.{{ .OriginalMethodName }}(ctx)
	{{- end }}
	{{- else }}
	{{- if .HasPayload }}
	// Create payload for methods that require it
	var payload {{ .PayloadType }}
	// TODO: Initialize payload fields as needed
	err := a.service.{{ .OriginalMethodName }}(ctx, payload)
	{{- else }}
	err := a.service.{{ .OriginalMethodName }}(ctx)
	{{- end }}
	{{- end }}
	
	if err != nil {
		return nil, err
	}

	{{- if .HasResult }}
	// Convert result to resource content
	resultData, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	return &{{ $.Package }}.ResourcesReadResult{
		Contents: []*{{ $.Package }}.ResourceContent{
			{
				URI:      "{{ .URI }}",
				MimeType: stringPtr("{{ .MimeType }}"),
				Text:     stringPtr(string(resultData)),
			},
		},
	}, nil
	{{- else }}
	return &{{ $.Package }}.ResourcesReadResult{
		Contents: []*{{ $.Package }}.ResourceContent{
			{
				URI:      "{{ .URI }}",
				MimeType: "{{ .MimeType }}",
				Text:     "{}",
			},
		},
	}, nil
	{{- end }}
}
{{- end }}

// initializeResourcesCache builds the static resources list
func (a *MCPAdapter) initializeResourcesCache() {
	a.resourcesCache = []*{{ .Package }}.ResourceInfo{
		{{- range .Resources }}
		{
			URI:         "{{ .URI }}",
			Name:        stringPtr("{{ .Name }}"),
			Description: stringPtr("{{ .Description }}"),
			MimeType:    stringPtr("{{ .MimeType }}"),
		},
		{{- end }}
	}
}
{{- end }}

{{- if or .StaticPrompts .DynamicPrompts }}

// PromptsList returns the list of available prompts
func (a *MCPAdapter) PromptsList(ctx context.Context, p *{{ .Package }}.PromptsListPayload) (*{{ .Package }}.PromptsListResult, error) {
	return &{{ .Package }}.PromptsListResult{
		Prompts: a.promptsCache,
	}, nil
}

// PromptsGet retrieves a specific prompt
func (a *MCPAdapter) PromptsGet(ctx context.Context, p *{{ .Package }}.PromptsGetPayload) (*{{ .Package }}.PromptsGetResult, error) {
{{- if or .StaticPrompts .DynamicPrompts }}
	if a.promptProvider == nil {
		return nil, fmt.Errorf("prompt provider not configured")
	}
{{- end }}
	
	{{- range .StaticPrompts }}
	if p.Name == "{{ .Name }}" {
		return a.promptProvider.Get{{ goify .Name }}Prompt(p.Arguments)
	}
	{{- end }}
	
	{{- range .DynamicPrompts }}
	if p.Name == "{{ .Name }}" {
		return a.promptProvider.Get{{ goify .Name }}Prompt(ctx, p.Arguments)
	}
	{{- end }}

	return nil, fmt.Errorf("unknown prompt: %s", p.Name)
}

// initializePromptsCache builds the static prompts list
func (a *MCPAdapter) initializePromptsCache() {
	a.promptsCache = []*{{ .Package }}.PromptInfo{
		{{- range .StaticPrompts }}
		{
			Name:        "{{ .Name }}",
			Description: stringPtr("{{ .Description }}"),
		},
		{{- end }}
		{{- range .DynamicPrompts }}
		{
			Name:        "{{ .Name }}",
			Description: stringPtr("{{ .Description }}"),
		},
		{{- end }}
	}
}
{{- end }}

// Subscribe to resource updates
func (a *MCPAdapter) Subscribe(ctx context.Context, p *{{ .Package }}.SubscribePayload) (*{{ .Package }}.SubscribeResult, error) {
	// In production, store subscription in a persistent store
	// For now, return success with a generated subscription ID
	return &{{ .Package }}.SubscribeResult{
		Success: true,
	}, nil
}

// Unsubscribe from resource updates
func (a *MCPAdapter) Unsubscribe(ctx context.Context, p *{{ .Package }}.UnsubscribePayload) (*{{ .Package }}.UnsubscribeResult, error) {
	// In production, remove subscription from persistent store
	// For now, always return success
	return &{{ .Package }}.UnsubscribeResult{
		Success: true,
	}, nil
}

// MonitorResourceChanges streams resource change events
func (a *MCPAdapter) MonitorResourceChanges(ctx context.Context, p *{{ .Package }}.MonitorResourceChangesPayload, stream {{ .Package }}.MonitorResourceChangesServerStream) error {
	// Send initial empty update
	if err := stream.Send(ctx, &{{ .Package }}.MonitorResourceChangesResult{
		Updates: []*{{ .Package }}.ResourceUpdate{},
	}); err != nil {
		return err
	}
	
	// In production, monitor actual resource changes
	// For demo, send periodic updates
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Send heartbeat update
			if err := stream.Send(ctx, &{{ .Package }}.MonitorResourceChangesResult{
				Updates: []*{{ .Package }}.ResourceUpdate{
					{
						UpdateID: "update_" + generateID(),
						Resource: "heartbeat",
						EventType: "ping",
						Data: nil,
						Timestamp: time.Now().Format(time.RFC3339),
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}

// StreamServerLogs streams server log events
func (a *MCPAdapter) StreamServerLogs(ctx context.Context, p *{{ .Package }}.StreamLogsPayload, stream {{ .Package }}.StreamServerLogsServerStream) error {
	// Send initial empty logs
	if err := stream.Send(ctx, &{{ .Package }}.StreamLogsResult{
		Logs: []*{{ .Package }}.LogEntry{},
	}); err != nil {
		return err
	}
	
	// In production, stream actual log events
	// For demo, send periodic log entries
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	
	counter := 0
	logLevels := []string{"debug", "info", "warn", "error"}
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			counter++
			level := logLevels[counter%len(logLevels)]
			if err := stream.Send(ctx, &{{ .Package }}.StreamLogsResult{
				Logs: []*{{ .Package }}.LogEntry{
					{
						Timestamp: time.Now().Format(time.RFC3339),
						Level: level,
						Message: fmt.Sprintf("Sample log message #%d", counter),
						Data: map[string]any{
							"component": "mcp-adapter",
							"iteration": counter,
						},
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}

// NotifyStatusUpdate sends status updates to the client
func (a *MCPAdapter) NotifyStatusUpdate(ctx context.Context, p *{{ .Package }}.SendNotificationPayload) error {
	// In production, send notification to connected clients via WebSocket or SSE
	// For now, log the notification
	fmt.Printf("[%s] Notification: %s (data: %v)\n", p.Type, p.Message, p.Data)
	
	return nil
}