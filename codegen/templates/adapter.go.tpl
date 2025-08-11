{{/* Header generated via codegen.Header */}}

{{ comment "MCPAdapter handles MCP protocol requests and adapts them to the original service" }}
type MCPAdapter struct {
	service {{ .Package }}.Service
	mux goahttp.Muxer
	promptProvider PromptProvider
}

{{ comment "NewMCPAdapter creates a new MCP adapter that wraps the original service" }}
func NewMCPAdapter(service {{ .Package }}.Service, promptProvider PromptProvider) *MCPAdapter {
	return &MCPAdapter{
		service: service,
		mux: goahttp.NewMuxer(),
		promptProvider: promptProvider,
	}
}

{{ comment "Initialize handles the MCP initialize request" }}
func (a *MCPAdapter) Initialize(ctx context.Context, p *InitializePayload) (*InitializeResult, error) {
	// Build server info
	serverInfo := &ServerInfo{
		Name:    "{{ .MCPName }}",
		Version: "{{ .MCPVersion }}",
	}

	// Build capabilities
	capabilities := &ServerCapabilities{}
	
	{{- if .Tools }}
	// Add tools capability
	capabilities.Tools = &ToolsCapability{}
	{{- end }}
	
	{{- if .Resources }}
	// Add resources capability
	capabilities.Resources = &ResourcesCapability{}
	{{- end }}
	
	{{- if or .StaticPrompts .DynamicPrompts }}
	// Add prompts capability
	capabilities.Prompts = &PromptsCapability{}
	{{- end }}
	
	return &InitializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      serverInfo,
		Capabilities:    capabilities,
	}, nil
}

{{ comment "Ping handles the MCP ping request" }}
func (a *MCPAdapter) Ping(ctx context.Context) error {
	// Simple ping response
	return nil
}

{{- if .Tools }}

{{ comment "ToolsList returns the list of available tools" }}
func (a *MCPAdapter) ToolsList(ctx context.Context) (*ToolsListResult, error) {
    tools := []*ToolInfo{
		{{- range .Tools }}
        {
            Name:        "{{ .Name }}",
			Description: stringPtr("{{ .Description }}"),
			{{- if .InputSchema }}
			InputSchema: json.RawMessage(`{{ .InputSchema }}`),
			{{- else }}
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {},
				"additionalProperties": false
			}`),
			{{- end }}
		},
		{{- end }}
	}
	
  return &ToolsListResult{Tools: tools}, nil
}

{{ comment "ToolsCall executes a tool and returns the result" }}
func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload) (*ToolsCallResult, error) {
	switch p.Name {
	{{- range .Tools }}
	case "{{ .Name }}":
		{{- if .HasPayload }}
		// Use HTTP decoder from the original service
		req := &http.Request{
			Body:   io.NopCloser(bytes.NewReader(p.Arguments)),
			Header: http.Header{"Content-Type": []string{"application/json"}},
		}
		
        // Decode using the original service's HTTP decoder
        dec := {{ $.Package }}http.Decode{{ .OriginalMethodName }}Request(a.mux, goahttp.RequestDecoder)
        payload, err := dec(req)
		if err != nil {
			return nil, fmt.Errorf("failed to decode arguments for tool {{ .Name }}: %w", err)
		}
		{{- end }}
		
		// Call the original service method
		{{- if .HasResult }}
		{{- if .HasPayload }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- else }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil {
			return nil, err
		}
		
        // Marshal the result to JSON
        resultBytes, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}
		{{- else }}
		{{- if .HasPayload }}
		err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- else }}
		err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil {
			return nil, err
		}
		
		// No result to return
		content := json.RawMessage(`{"status": "success"}`)
		{{- end }}
		
        txt := stringPtr(string(resultBytes))
        return &ToolsCallResult{Content: []*ContentItem{ { Type: "text", Text: txt } }}, nil
	{{- end }}
	default:
		return nil, fmt.Errorf("unknown tool: %s", p.Name)
	}
}
{{- end }}

{{- if .Resources }}

{{ comment "ResourcesList returns the list of available resources" }}
func (a *MCPAdapter) ResourcesList(ctx context.Context) (*ResourcesListResult, error) {
    resources := []*ResourceInfo{
		{{- range .Resources }}
		{
			URI:         "{{ .URI }}",
            Name:        stringPtr("{{ .Name }}"),
			Description: stringPtr("{{ .Description }}"),
			MimeType:    stringPtr("{{ .MimeType }}"),
		},
		{{- end }}
	}
	
    return &ResourcesListResult{Resources: resources}, nil
}

{{ comment "ResourcesRead reads a resource and returns its content" }}
func (a *MCPAdapter) ResourcesRead(ctx context.Context, p *ResourcesReadPayload) (*ResourcesReadResult, error) {
	switch p.URI {
	{{- range .Resources }}
	case "{{ .URI }}":
		{{- if .HasPayload }}
		// For resources, encode the URI into a JSON payload for the decoder
		argBytes, err := json.Marshal(map[string]string{"uri": p.URI})
		if err != nil {
			return nil, fmt.Errorf("failed to encode URI: %w", err)
		}
		
		req := &http.Request{
			Body:   io.NopCloser(bytes.NewReader(argBytes)),
			Header: http.Header{"Content-Type": []string{"application/json"}},
		}
		
        // Decode using the original service's HTTP decoder
        dec := {{ $.Package }}http.Decode{{ .OriginalMethodName }}Request(a.mux, goahttp.RequestDecoder)
        payload, err := dec(req)
		if err != nil {
			return nil, fmt.Errorf("failed to decode payload for resource {{ .URI }}: %w", err)
		}
		{{- end }}
		
		// Call the original service method
		{{- if .HasResult }}
		{{- if .HasPayload }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- else }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil {
			return nil, err
		}
		
        // Marshal the result to JSON
        resultBytes, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}
		{{- else }}
		{{- if .HasPayload }}
		err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- else }}
		err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil {
			return nil, err
		}
		
		// No result to return
		content := json.RawMessage(`{"status": "success"}`)
		{{- end }}
		
        return &ResourcesReadResult{
            Contents: []*ResourceContent{
                { URI: p.URI, MimeType: stringPtr("{{ .MimeType }}"), Text: stringPtr(string(resultBytes)) },
            },
        }, nil
	{{- end }}
	default:
		return nil, fmt.Errorf("unknown resource: %s", p.URI)
	}
}

{{ comment "ResourcesSubscribe subscribes to resource changes" }}
func (a *MCPAdapter) ResourcesSubscribe(ctx context.Context, p *ResourcesSubscribePayload) error {
	// TODO: Implement resource subscription if needed
	return fmt.Errorf("resource subscription not implemented")
}

{{ comment "ResourcesUnsubscribe unsubscribes from resource changes" }}
func (a *MCPAdapter) ResourcesUnsubscribe(ctx context.Context, p *ResourcesUnsubscribePayload) error {
	// TODO: Implement resource unsubscription if needed
	return fmt.Errorf("resource unsubscription not implemented")
}
{{- end }}

{{- if or .StaticPrompts .DynamicPrompts }}

{{ comment "PromptsList returns the list of available prompts" }}
func (a *MCPAdapter) PromptsList(ctx context.Context) (*PromptsListResult, error) {
    prompts := []*PromptInfo{
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
	
    return &PromptsListResult{Prompts: prompts}, nil
}

{{ comment "PromptsGet retrieves a specific prompt" }}
func (a *MCPAdapter) PromptsGet(ctx context.Context, p *PromptsGetPayload) (*PromptsGetResult, error) {
	{{- if .StaticPrompts }}
	if a.promptProvider == nil {
		return nil, fmt.Errorf("prompt provider not configured")
	}
	{{- end }}
	
	switch p.Name {
	{{- range .StaticPrompts }}
	case "{{ .Name }}":
		return a.promptProvider.Get{{ goify .Name }}Prompt(p.Arguments)
	{{- end }}
	{{- range .DynamicPrompts }}
	case "{{ .Name }}":
		{{- if .HasPayload }}
		// Use HTTP decoder from the original service
		req := &http.Request{
			Body:   io.NopCloser(bytes.NewReader(p.Arguments)),
			Header: http.Header{"Content-Type": []string{"application/json"}},
		}
		
        // Decode using the original service's HTTP decoder
        dec := {{ $.Package }}http.Decode{{ .OriginalMethodName }}Request(a.mux, goahttp.RequestDecoder)
        payload, err := dec(req)
		if err != nil {
			return nil, fmt.Errorf("failed to decode arguments for prompt {{ .Name }}: %w", err)
		}
		
		// Call the original service method
		result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- else }}
		// Call the original service method
		result, err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil {
			return nil, err
		}
		
        // Convert service result into a single text prompt message
        resultBytes, err := json.Marshal(result)
        if err != nil { return nil, fmt.Errorf("failed to marshal result: %w", err) }
        return &PromptsGetResult{
            Description: stringPtr("{{ .Description }}"),
            Messages: []*PromptMessage{
                { Role: "system", Content: &MessageContent{ Type: "text", Text: stringPtr(string(resultBytes)) } },
            },
        }, nil
	{{- end }}
	default:
		return nil, fmt.Errorf("unknown prompt: %s", p.Name)
	}
}
{{- end }}

{{ comment "stringPtr returns a pointer to a string" }}
func stringPtr(s string) *string {
	return &s
}