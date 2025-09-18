{{/* Header generated via codegen.Header */}}

{{ comment "MCPAdapter handles MCP protocol requests and adapts them to the original service" }}
// Required imports:
// - bytes, context, encoding/json, io, net/http, sync
// - goahttp, jsonrpc, goa and original service packages are included via header
type MCPAdapter struct {
    service {{ .Package }}.Service
    mux goahttp.Muxer
    initialized bool
    mu sync.RWMutex
    opts *MCPAdapterOptions
    {{- if or .StaticPrompts .DynamicPrompts }}
    promptProvider PromptProvider
    {{- end }}
}

// MCPAdapterOptions allows customizing adapter behavior.
type MCPAdapterOptions struct {
    // Logger is an optional hook called with internal adapter events.
    // event examples: "request", "response", "error"; details is implementation-defined.
    Logger func(ctx context.Context, event string, details any)
    // ErrorMapper allows mapping arbitrary errors to framework-friendly errors
    // (e.g., goa.PermanentError with specific JSON-RPC codes).
    ErrorMapper func(error) error
    // Allowed/Deny lists for resource URIs. If AllowedResourceURIs is non-empty,
    // only URIs in that list are permitted. DeniedResourceURIs takes precedence.
    AllowedResourceURIs []string
    DeniedResourceURIs  []string
}

// supportedProtocolVersion defines the MCP protocol version this adapter expects.
const supportedProtocolVersion = "2025-06-18"

// bufferResponseWriter is a minimal http.ResponseWriter that writes to an in-memory buffer
// used to leverage goa encoders without an actual HTTP response.
type bufferResponseWriter struct {
	headers http.Header
	buf     bytes.Buffer
}

func (w *bufferResponseWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}
func (w *bufferResponseWriter) WriteHeader(statusCode int) {}
func (w *bufferResponseWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func encodeJSONToString(ctx context.Context, v any) (string, error) {
    bw := &bufferResponseWriter{}
    if err := goahttp.ResponseEncoder(ctx, bw).Encode(v); err != nil {
        return "", err
    }
    return bw.buf.String(), nil
}

// parseQueryParamsToJSON parses the query parameters of a URI into a JSON
// object where keys are parameter names and values are best-effort typed.
// Repeated parameters become arrays. Numbers and booleans are detected.
func parseQueryParamsToJSON(uri string) ([]byte, error) {
    u, err := url.Parse(uri)
    if err != nil {
        return nil, fmt.Errorf("invalid resource URI: %w", err)
    }
    q := u.Query()
    if len(q) == 0 {
        return []byte("{}"), nil
    }
    m := make(map[string]any, len(q))
    for k, vals := range q {
        if len(vals) == 1 {
            m[k] = coerce(vals[0])
            continue
        }
        arr := make([]any, len(vals))
        for i := range vals {
            arr[i] = coerce(vals[i])
        }
        m[k] = arr
    }
    return json.Marshal(m)
}

// coerce tries to interpret s as bool, int, float, else returns s.
func coerce(s string) any {
    ls := strings.ToLower(s)
    switch ls {
    case "true", "t", "1":
        return true
    case "false", "f", "0":
        return false
    }
    if i, err := strconv.ParseInt(s, 10, 64); err == nil {
        return i
    }
    if f, err := strconv.ParseFloat(s, 64); err == nil {
        return f
    }
    return s
}

{{ comment "NewMCPAdapter creates a new MCP adapter that wraps the original service" }}
func NewMCPAdapter(service {{ .Package }}.Service{{ if or .StaticPrompts .DynamicPrompts }}, promptProvider PromptProvider{{ end }}, opts *MCPAdapterOptions) *MCPAdapter {
    return &MCPAdapter{
        service: service,
        mux: goahttp.NewMuxer(),
        opts: opts,
        {{- if or .StaticPrompts .DynamicPrompts }}
        promptProvider: promptProvider,
        {{- end }}
    }
}

{{ comment "Initialize handles the MCP initialize request" }}
func (a *MCPAdapter) Initialize(ctx context.Context, p *InitializePayload) (*InitializeResult, error) {
    if p == nil || p.ProtocolVersion == "" {
        return nil, goa.PermanentError("invalid_params", "Missing protocolVersion")
    }
    switch p.ProtocolVersion {
    case supportedProtocolVersion:
    default:
        return nil, goa.PermanentError("invalid_params", "Unsupported protocol version")
    }
    a.mu.Lock()
    if a.initialized {
        a.mu.Unlock()
        return nil, goa.PermanentError("invalid_params", "Already initialized")
    }
    a.initialized = true
    a.mu.Unlock()
	// Build server info
    serverInfo := &ServerInfo{
		Name:    {{ quote .MCPName }},
		Version: {{ quote .MCPVersion }},
	}
	// Build capabilities
	capabilities := &ServerCapabilities{}
	{{- if .Tools }}
	capabilities.Tools = &ToolsCapability{}
	{{- end }}
	{{- if .Resources }}
	capabilities.Resources = &ResourcesCapability{}
	{{- end }}
	{{- if or .StaticPrompts .DynamicPrompts }}
	capabilities.Prompts = &PromptsCapability{}
	{{- end }}
    return &InitializeResult{
        ProtocolVersion: supportedProtocolVersion,
        ServerInfo:      serverInfo,
        Capabilities:    capabilities,
    }, nil
}

func (a *MCPAdapter) isInitialized() bool {
    a.mu.RLock()
    ok := a.initialized
    a.mu.RUnlock()
    return ok
}

{{ comment "Ping handles the MCP ping request" }}
func (a *MCPAdapter) Ping(ctx context.Context) (*PingResult, error) {
	return &PingResult{Pong: true}, nil
}

{{- if .Tools }}

{{ comment "ToolsList returns the list of available tools" }}
func (a *MCPAdapter) ToolsList(ctx context.Context, p *ToolsListPayload) (*ToolsListResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    tools := []*ToolInfo{
		{{- range .Tools }}
        {
            Name:        {{ quote .Name }},
			Description: stringPtr({{ quote .Description }}),
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

{{ if .ToolsCallStreaming }}
{{ comment "Stream bridges from original server-streaming methods to MCP ToolsCall stream" }}
{{- range .Tools }}
{{- if .IsStreaming }}
type {{ goify .OriginalMethodName }}StreamBridge struct { out ToolsCallServerStream; sent bool; mu sync.Mutex }
func (b *{{ goify .OriginalMethodName }}StreamBridge) Send(ctx context.Context, ev {{ $.Package }}.{{ .StreamEventType }}) error {
    b.mu.Lock(); b.sent = true; b.mu.Unlock()
    s, serr := encodeJSONToString(ctx, ev)
    if serr != nil { return serr }
    return b.out.Send(ctx, &ToolsCallResult{ Content: []*ContentItem{ &ContentItem{ Type: "text", Text: &s } } })
}
func (b *{{ goify .OriginalMethodName }}StreamBridge) SendAndClose(ctx context.Context, ev {{ $.Package }}.{{ .StreamEventType }}) error {
    b.mu.Lock(); b.sent = true; b.mu.Unlock()
    s, serr := encodeJSONToString(ctx, ev)
    if serr != nil { return serr }
    return b.out.SendAndClose(ctx, &ToolsCallResult{ Content: []*ContentItem{ &ContentItem{ Type: "text", Text: &s } } })
}
func (b *{{ goify .OriginalMethodName }}StreamBridge) SendError(ctx context.Context, id string, err error) error {
    return b.out.SendError(ctx, id, err)
}
{{- end }}
{{- end }}
{{ end }}

{{ if .ToolsCallStreaming }}
{{ comment "ToolsCall executes a tool and streams progress and final result when requested via SSE" }}
func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload, stream ToolsCallServerStream) error {
    if !a.isInitialized() { return goa.PermanentError("invalid_params", "Not initialized") }
    switch p.Name {
    {{- range .Tools }}
    case {{ quote .Name }}:
        {{- if .HasPayload }}
        // Decode arguments into original payload using goa HTTP decoder
        req := &http.Request{ Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(p.Arguments)) }
        {{- if .IsStreaming }}
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil { return goa.PermanentError("invalid_params", "%s", err.Error()) }
        {{- if .RequiredFields }}
        // Required fields check (top-level)
        {
            {{- range .RequiredFields }}
            if payload.{{ goify . }} == "" { return goa.PermanentError("invalid_params", "Missing required field: {{ . }}") }
            {{- end }}
        }
        {{- end }}
        {{- if .EnumFields }}
        // Enum fields check (top-level)
        {
            {{- $tool := . }}
            {{- range $fname, $vals := .EnumFields }}
            {
                var __val string
                {{- if (index $tool.EnumFieldsPtr $fname) }}
                if payload.{{ goify $fname }} != nil { __val = *payload.{{ goify $fname }} }
                {{- else }}
                __val = payload.{{ goify $fname }}
                {{- end }}
                ok := false
                switch __val {
                {{- range $vals }}
                case {{ printf "%q" . }}:
                    ok = true
                {{- end }}
                }
                if !ok && __val != "" { return goa.PermanentError("invalid_params", "Invalid value for {{ $fname }}") }
            }
            {{- end }}
        }
        {{- end }}
        // Bridge original server stream interface to MCP ToolsCall stream
        bridge := &{{ goify .OriginalMethodName }}StreamBridge{ out: stream }
        if err := a.service.{{ .OriginalMethodName }}(ctx, payload, bridge); err != nil { return err }
        return nil
        {{- else }}
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil { return goa.PermanentError("invalid_params", "%s", err.Error()) }
        {{- if .RequiredFields }}
        {
            {{- range .RequiredFields }}
            if payload.{{ goify . }} == "" { return goa.PermanentError("invalid_params", "Missing required field: {{ . }}") }
            {{- end }}
        }
        {{- end }}
        {{- if .EnumFields }}
        {
            {{- $tool := . }}
            {{- range $fname, $vals := .EnumFields }}
            {
                var __val string
                {{- if (index $tool.EnumFieldsPtr $fname) }}
                if payload.{{ goify $fname }} != nil { __val = *payload.{{ goify $fname }} }
                {{- else }}
                __val = payload.{{ goify $fname }}
                {{- end }}
                ok := false
                switch __val {
                {{- range $vals }}
                case {{ printf "%q" . }}:
                    ok = true
                {{- end }}
                }
                if !ok && __val != "" { return goa.PermanentError("invalid_params", "Invalid value for {{ $fname }}") }
            }
            {{- end }}
        }
        {{- end }}
        {{- end }}
        {{- end }}
        {{- if not .IsStreaming }}
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
        {{- else }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx)
        {{- end }}
        if err != nil { return err }
        // Encode result to JSON string using goa encoder
        s, serr := encodeJSONToString(ctx, result)
        if serr != nil { return serr }
        // Emit final response and close stream
        final := &ToolsCallResult{ Content: []*ContentItem{ &ContentItem{ Type: "text", Text: &s } } }
        return stream.SendAndClose(ctx, final)
        {{- else }}
        {{- if .HasPayload }}
        if err := a.service.{{ .OriginalMethodName }}(ctx, payload); err != nil { return err }
        {{- else }}
        if err := a.service.{{ .OriginalMethodName }}(ctx); err != nil { return err }
        {{- end }}
        ok := stringPtr("{\"status\":\"success\"}")
        return stream.SendAndClose(ctx, &ToolsCallResult{ Content: []*ContentItem{ &ContentItem{ Type: "text", Text: ok } } })
        {{- end }}
        {{- end }}
    {{- end }}
    default:
        return goa.PermanentError("method_not_found", "Unknown tool: %s", p.Name)
    }
}
{{ else }}
{{ comment "ToolsCall executes a tool and returns the result (non-streaming)" }}
func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload) (*ToolsCallResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
	switch p.Name {
	{{- range .Tools }}
	case {{ quote .Name }}:
		{{- if .HasPayload }}
		req := &http.Request{ Body: io.NopCloser(bytes.NewReader(p.Arguments)), Header: http.Header{"Content-Type": []string{"application/json"}}, }
		var payload {{ .PayloadType }}
		if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil { return nil, goa.PermanentError("invalid_params", "%s", err.Error()) }
		{{- end }}
		{{- if .HasResult }}
		{{- if .HasPayload }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- else }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil { return nil, err }
		s, serr := encodeJSONToString(ctx, result)
		if serr != nil { return nil, goa.PermanentError("invalid_params", "%s", serr.Error()) }
		return &ToolsCallResult{ Content: []*ContentItem{ &ContentItem{ Type: "text", Text: &s } } }, nil
		{{- else }}
		{{- if .HasPayload }}
		if err := a.service.{{ .OriginalMethodName }}(ctx, payload); err != nil { return nil, err }
		{{- else }}
		if err := a.service.{{ .OriginalMethodName }}(ctx); err != nil { return nil, err }
		{{- end }}
		ok := stringPtr("{\"status\":\"success\"}")
		return &ToolsCallResult{ Content: []*ContentItem{ &ContentItem{ Type: "text", Text: ok } } }, nil
		{{- end }}
	{{- end }}
    default:
        return nil, goa.PermanentError("method_not_found", "Unknown tool: %s", p.Name)
	}
}
{{ end }}
{{- end }}

{{- if .Resources }}

{{ comment "ResourcesList returns the list of available resources" }}
func (a *MCPAdapter) ResourcesList(ctx context.Context, p *ResourcesListPayload) (*ResourcesListResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    resources := []*ResourceInfo{
			{{- range .Resources }}
			{
				URI:         {{ quote .URI }},
            Name:        stringPtr({{ quote .Name }}),
				Description: stringPtr({{ quote .Description }}),
				MimeType:    stringPtr({{ quote .MimeType }}),
			},
			{{- end }}
		}
	return &ResourcesListResult{Resources: resources}, nil
}

{{ comment "ResourcesRead reads a resource and returns its content" }}
func (a *MCPAdapter) ResourcesRead(ctx context.Context, p *ResourcesReadPayload) (*ResourcesReadResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    if err := a.assertResourceURIAllowed(p.URI); err != nil { return nil, goa.PermanentError("invalid_params", "%s", err.Error()) }
    baseURI := p.URI
    if i := strings.Index(baseURI, "?"); i >= 0 { baseURI = baseURI[:i] }
    switch baseURI {
{{- range .Resources }}
    case {{ quote .URI }}:
        {{- if .HasPayload }}
        // Map URI query parameters to original payload and decode
        args, aerr := parseQueryParamsToJSON(p.URI)
        if aerr != nil { return nil, goa.PermanentError("invalid_params", "%s", aerr.Error()) }
        req := &http.Request{ Body: io.NopCloser(bytes.NewReader(args)), Header: http.Header{"Content-Type": []string{"application/json"}}, }
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil { return nil, goa.PermanentError("invalid_params", "%s", err.Error()) }
        {{- end }}
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
        {{- else }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil { return nil, err }
		s, serr := encodeJSONToString(ctx, result)
		if serr != nil { return nil, goa.PermanentError("invalid_params", "%s", serr.Error()) }
			return &ResourcesReadResult{ Contents: []*ResourceContent{ { URI: baseURI, MimeType: stringPtr({{ quote .MimeType }}), Text: &s } } }, nil
		{{- else }}
		{{- if .HasPayload }}
		if err := a.service.{{ .OriginalMethodName }}(ctx, payload); err != nil { return nil, err }
		{{- else }}
		if err := a.service.{{ .OriginalMethodName }}(ctx); err != nil { return nil, err }
		{{- end }}
        return &ResourcesReadResult{ Contents: []*ResourceContent{ { URI: baseURI, MimeType: stringPtr({{ quote .MimeType }}), Text: stringPtr("{\"status\":\"success\"}") } } }, nil
		{{- end }}
	{{- end }}
    default:
        return nil, goa.PermanentError("method_not_found", "Unknown resource: %s", p.URI)
	}
}

// assertResourceURIAllowed verifies pURI passes allow/deny filters when configured.
func (a *MCPAdapter) assertResourceURIAllowed(pURI string) error {
    if a == nil || a.opts == nil {
        return nil
    }
    // Deny list takes precedence
    for _, d := range a.opts.DeniedResourceURIs {
        if d == pURI {
            return fmt.Errorf("resource URI denied: %s", pURI)
        }
    }
    if len(a.opts.AllowedResourceURIs) == 0 {
        return nil
    }
    for _, allow := range a.opts.AllowedResourceURIs {
        if allow == pURI {
            return nil
        }
    }
    return fmt.Errorf("resource URI not allowed: %s", pURI)
}

{{ comment "ResourcesSubscribe subscribes to resource changes" }}
func (a *MCPAdapter) ResourcesSubscribe(ctx context.Context, p *ResourcesSubscribePayload) error {
    if !a.isInitialized() { return goa.PermanentError("invalid_params", "Not initialized") }
    return nil
}

{{ comment "ResourcesUnsubscribe unsubscribes from resource changes" }}
func (a *MCPAdapter) ResourcesUnsubscribe(ctx context.Context, p *ResourcesUnsubscribePayload) error {
    if !a.isInitialized() { return goa.PermanentError("invalid_params", "Not initialized") }
    return nil
}
{{- end }}

{{- if or .StaticPrompts .DynamicPrompts }}

{{ comment "PromptsList returns the list of available prompts" }}
func (a *MCPAdapter) PromptsList(ctx context.Context, p *PromptsListPayload) (*PromptsListResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    prompts := []*PromptInfo{
        {{- range .DynamicPrompts }}
        {
            Name:        {{ quote .Name }},
            Description: stringPtr({{ quote .Description }}),
            Arguments: []*PromptArgument{
                {{- range .Arguments }}
                { Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), Required: {{ .Required }} },
                {{- end }}
            },
        },
        {{- end }}
        {{- range .StaticPrompts }}
        {
            Name:        {{ quote .Name }},
            Description: stringPtr({{ quote .Description }}),
            // No explicit arguments for static prompts
        },
        {{- end }}
    }
    return &PromptsListResult{Prompts: prompts}, nil
}

{{ comment "PromptsGet resolves static prompts directly and delegates dynamic prompts to the provider" }}
func (a *MCPAdapter) PromptsGet(ctx context.Context, p *PromptsGetPayload) (*PromptsGetResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    if p == nil || p.Name == "" { return nil, goa.PermanentError("invalid_params", "Missing prompt name") }

    // Static prompts handled inline with optional provider override
    switch p.Name {
    {{- range .StaticPrompts }}
    case "{{ .Name }}":
        if a.promptProvider != nil {
            if res, err := a.promptProvider.Get{{ goify .Name }}Prompt(p.Arguments); err == nil && res != nil {
                return res, nil
            } else if err != nil { return nil, err }
        }
        // Fallback to generated static prompt content
        msgs := make([]*PromptMessage, 0, {{ len .Messages }})
        {{- range .Messages }}
        msgs = append(msgs, &PromptMessage{ Role: {{ quote .Role }}, Content: &MessageContent{ Type: "text", Text: stringPtr({{ quote .Content }}) } })
        {{- end }}
        return &PromptsGetResult{ Description: stringPtr({{ quote .Description }}), Messages: msgs }, nil
    {{- end }}
    }

    // Dynamic prompts require a provider implementation
    {{- if .DynamicPrompts }}
    if a.promptProvider == nil {
        return nil, goa.PermanentError("invalid_params", "No prompt provider configured for dynamic prompts")
    }
    switch p.Name {
    {{- range .DynamicPrompts }}
    case "{{ .Name }}":
        return a.promptProvider.Get{{ goify .Name }}Prompt(ctx, p.Arguments)
    {{- end }}
    }
    {{- end }}

    return nil, goa.PermanentError("method_not_found", "Unknown prompt: %s", p.Name)
}
{{- end }}

{{- if .Notifications }}
{{ comment "NotifyStatusUpdate handles notifications with no response" }}
func (a *MCPAdapter) NotifyStatusUpdate(ctx context.Context, p *NotifyStatusUpdatePayload) error {
    return nil
}
{{- end }}

{{- if .Subscriptions }}
{{ comment "Subscribe returns a default success for demo purposes" }}
func (a *MCPAdapter) Subscribe(ctx context.Context, p *SubscribePayload) (*SubscribeResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    return &SubscribeResult{Success: true}, nil
}

{{ comment "Unsubscribe returns a default success for demo purposes" }}
func (a *MCPAdapter) Unsubscribe(ctx context.Context, p *UnsubscribePayload) (*UnsubscribeResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    return &UnsubscribeResult{Success: true}, nil
}
{{- end }}

{{ comment "stringPtr returns a pointer to a string" }}
func stringPtr(s string) *string {
	return &s
}
