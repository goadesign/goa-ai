{{ comment "MCPAdapter core: types, options, constructor, helpers" }}

type MCPAdapter struct {
    service {{ .Package }}.Service
    initialized bool
    mu sync.RWMutex
    opts *MCPAdapterOptions
    {{- if or .StaticPrompts .DynamicPrompts }}
    promptProvider PromptProvider
    {{- end }}
    // Minimal subscription registry keyed by resource URI
    subs   map[string]int
    subsMu sync.Mutex
    // Broadcaster for server-initiated events (notifications/resources)
    broadcaster Broadcaster
    // resourceNameToURI holds DSL-derived mapping for policy and lookups
    resourceNameToURI map[string]string
}

// MCPAdapterOptions allows customizing adapter behavior.
type MCPAdapterOptions struct {
    // Logger is an optional hook called with internal adapter events.
    Logger func(ctx context.Context, event string, details any)
    // ErrorMapper allows mapping arbitrary errors to framework-friendly errors
    ErrorMapper func(error) error
    // Allowed/Deny lists for resource URIs; Denied takes precedence unless header allow overrides
    AllowedResourceURIs []string
    DeniedResourceURIs  []string
    // Name-based policy resolved to URIs at construction
    AllowedResourceNames []string
    DeniedResourceNames  []string
    StructuredStreamJSON bool
    ProtocolVersionOverride string
    // Pluggable broadcaster, else default channel broadcaster
    Broadcaster Broadcaster
    BroadcastBuffer int
    DropIfSlow bool
}

// mcpProtocolVersion resolves the protocol version from options or default.
func (a *MCPAdapter) mcpProtocolVersion() string {
    if a != nil && a.opts != nil && a.opts.ProtocolVersionOverride != "" {
        return a.opts.ProtocolVersionOverride
    }
    return DefaultProtocolVersion
}

// bufferResponseWriter writes to a buffer to reuse Goa encoders without HTTP response.
type bufferResponseWriter struct {
    headers http.Header
    buf     bytes.Buffer
}

func (w *bufferResponseWriter) Header() http.Header { if w.headers == nil { w.headers = make(http.Header) }; return w.headers }
func (w *bufferResponseWriter) WriteHeader(statusCode int) {}
func (w *bufferResponseWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func encodeJSONToString(ctx context.Context, v any) (string, error) {
    bw := &bufferResponseWriter{}
    if err := goahttp.ResponseEncoder(ctx, bw).Encode(v); err != nil { return "", err }
    return bw.buf.String(), nil
}

// parseQueryParamsToJSON converts URI query params into JSON.
func parseQueryParamsToJSON(uri string) ([]byte, error) {
    u, err := url.Parse(uri)
    if err != nil { return nil, fmt.Errorf("invalid resource URI: %w", err) }
    q := u.Query(); if len(q) == 0 { return []byte("{}"), nil }
    m := make(map[string]any, len(q))
    for k, vals := range q {
        if len(vals) == 1 { m[k] = coerce(vals[0]); continue }
        arr := make([]any, len(vals)); for i := range vals { arr[i] = coerce(vals[i]) }; m[k] = arr
    }
    return json.Marshal(m)
}

func coerce(s string) any {
    ls := strings.ToLower(s)
    switch ls {
    case "true","t","1": return true
    case "false","f","0": return false
    }
    if i, err := strconv.ParseInt(s, 10, 64); err == nil {
        return i
    }
    if f, err := strconv.ParseFloat(s, 64); err == nil {
        return f
    }
    return s
}

func NewMCPAdapter(service {{ .Package }}.Service{{ if or .StaticPrompts .DynamicPrompts }}, promptProvider PromptProvider{{ end }}, opts *MCPAdapterOptions) *MCPAdapter {
    // Resolve name-based policy to URIs
    if opts != nil && (len(opts.AllowedResourceNames) > 0 || len(opts.DeniedResourceNames) > 0) {
        nameToURI := map[string]string{
            {{- range .Resources }}
            {{ printf "%q" .Name }}: {{ printf "%q" .URI }},
            {{- end }}
        }
        seen := map[string]struct{}{}
        for _, n := range opts.AllowedResourceNames {
            if u, ok := nameToURI[n]; ok {
                if _, dup := seen["allow:"+u]; !dup {
                    opts.AllowedResourceURIs = append(opts.AllowedResourceURIs, u)
                    seen["allow:"+u] = struct{}{}
                }
            }
        }
        for _, n := range opts.DeniedResourceNames {
            if u, ok := nameToURI[n]; ok {
                if _, dup := seen["deny:"+u]; !dup {
                    opts.DeniedResourceURIs = append(opts.DeniedResourceURIs, u)
                    seen["deny:"+u] = struct{}{}
                }
            }
        }
    }
    // Broadcaster
    var bc Broadcaster
    if opts != nil && opts.Broadcaster != nil {
        bc = opts.Broadcaster
    } else {
        buf := 32
        drop := true
        if opts != nil {
            if opts.BroadcastBuffer > 0 {
                buf = opts.BroadcastBuffer
            }
            if opts.DropIfSlow == false {
                drop = false
            }
        }
        bc = newChannelBroadcaster(buf, drop)
    }
    // Build name->URI map from generated resources
    nameToURI := map[string]string{
        {{- range .Resources }}
        {{ printf "%q" .Name }}: {{ printf "%q" .URI }},
        {{- end }}
    }
    return &MCPAdapter{
        service: service,
        opts: opts,
        {{- if or .StaticPrompts .DynamicPrompts }}
        promptProvider: promptProvider,
        {{- end }}
        subs: make(map[string]int),
        broadcaster: bc,
        resourceNameToURI: nameToURI,
    }
}

func (a *MCPAdapter) isInitialized() bool {
    a.mu.RLock()
    ok := a.initialized
    a.mu.RUnlock()
    return ok
}

func (a *MCPAdapter) log(ctx context.Context, event string, details any) {
    if a != nil && a.opts != nil && a.opts.Logger != nil {
        a.opts.Logger(ctx, event, details)
    }
}

func (a *MCPAdapter) mapError(err error) error {
    if a != nil && a.opts != nil && a.opts.ErrorMapper != nil && err != nil {
        if m := a.opts.ErrorMapper(err); m != nil { return m }
    }
    return err
}

func stringPtr(s string) *string { return &s }

func isLikelyJSON(s string) bool { return json.Valid([]byte(s)) }

// buildContentItem returns a ContentItem honoring StructuredStreamJSON option.
func buildContentItem(a *MCPAdapter, s string) *ContentItem {
    if a != nil && a.opts != nil && a.opts.StructuredStreamJSON && isLikelyJSON(s) {
        mt := stringPtr("application/json")
        return &ContentItem{ Type: "text", MimeType: mt, Text: &s }
    }
    return &ContentItem{ Type: "text", Text: &s }
}

// Initialize handles the MCP initialize request.
func (a *MCPAdapter) Initialize(ctx context.Context, p *InitializePayload) (*InitializeResult, error) {
    if p == nil || p.ProtocolVersion == "" {
        return nil, goa.PermanentError("invalid_params", "Missing protocolVersion")
    }
    switch p.ProtocolVersion {
    case a.mcpProtocolVersion():
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

    serverInfo := &ServerInfo{
        Name:    {{ quote .MCPName }},
        Version: {{ quote .MCPVersion }},
    }

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
        ProtocolVersion: a.mcpProtocolVersion(),
        ServerInfo:      serverInfo,
        Capabilities:    capabilities,
    }, nil
}

// Ping handles the MCP ping request.
func (a *MCPAdapter) Ping(ctx context.Context) (*PingResult, error) {
    a.log(ctx, "request", map[string]any{"method": "ping"})
    res := &PingResult{Pong: true}
    a.log(ctx, "response", map[string]any{"method": "ping"})
    return res, nil
}


