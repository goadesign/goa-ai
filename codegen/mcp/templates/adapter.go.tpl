{{/* Header generated via codegen.Header */}}

{{ comment "MCPAdapter handles MCP protocol requests and adapts them to the original service" }}

{{- /* Core: struct, options, constructor, common helpers, Initialize, Ping */ -}}
{{ mcpTemplates.Read "adapter_core" }}

{{- /* Broadcast primitives and Publish helpers */ -}}
{{ mcpTemplates.Read "adapter_broadcast" }}

{{- /* Tools: list/call and streaming bridges */ -}}
{{ mcpTemplates.Read "adapter_tools" }}

{{- /* Resources: list/read, allow/deny policy, subscribe/unsubscribe */ -}}
{{ mcpTemplates.Read "adapter_resources" }}

{{- /* Prompts: list/get (static + dynamic) */ -}}
{{ mcpTemplates.Read "adapter_prompts" }}

{{- /* Notifications + events/stream SSE */ -}}
{{ mcpTemplates.Read "adapter_notifications" }}

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
    // Optional resource name-based policy. Names are resolved to URIs at construction
    // time using the resource table generated from the design.
    AllowedResourceNames []string
    DeniedResourceNames  []string
    StructuredStreamJSON bool
    ProtocolVersionOverride string
    // Broadcaster allows plugging a custom event broadcaster. If nil, a default
    // channel-based broadcaster is used.
    Broadcaster Broadcaster
    // BroadcastBuffer defines the buffer size for default broadcaster
    // subscriptions. Default is 32.
    BroadcastBuffer int
    // DropIfSlow controls whether the default broadcaster drops events when
    // a subscriber is slow (true) or blocks (false). Default true.
    DropIfSlow bool
}

// mcpProtocolVersion resolves the protocol version from options or default.
func (a *MCPAdapter) mcpProtocolVersion() string {
    if a != nil && a.opts != nil && a.opts.ProtocolVersionOverride != "" {
        return a.opts.ProtocolVersionOverride
    }
    return DefaultProtocolVersion
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
    // If name-based allow/deny lists are provided, resolve to URIs using the
    // generated resource table.
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
    // Default broadcaster
    var bc Broadcaster
    if opts != nil && opts.Broadcaster != nil {
        bc = opts.Broadcaster
    } else {
        buf := 32
        drop := true
        if opts != nil {
            if opts.BroadcastBuffer > 0 { buf = opts.BroadcastBuffer }
            if opts.DropIfSlow == false { drop = false }
        }
        bc = newChannelBroadcaster(buf, drop)
    }
    return &MCPAdapter{
        service: service,
        opts: opts,
        {{- if or .StaticPrompts .DynamicPrompts }}
        promptProvider: promptProvider,
        {{- end }}
        subs: make(map[string]int),
        broadcaster: bc,
    }
}

// Broadcaster defines a simple publish/subscribe API for server-initiated events.
type Broadcaster interface {
    Subscribe(ctx context.Context) (Subscription, error)
    Publish(ev *ToolsCallResult)
    Close() error
}

// Subscription represents a subscriber to broadcast events.
type Subscription interface {
    C() <-chan *ToolsCallResult
    Close() error
}

// channelBroadcaster is a default in-memory broadcaster.
type channelBroadcaster struct {
    mu    sync.RWMutex
    subs  map[chan *ToolsCallResult]struct{}
    buf   int
    drop  bool
    closed bool
}

func newChannelBroadcaster(buf int, drop bool) *channelBroadcaster {
    return &channelBroadcaster{subs: make(map[chan *ToolsCallResult]struct{}), buf: buf, drop: drop}
}

func (b *channelBroadcaster) Subscribe(ctx context.Context) (Subscription, error) {
    ch := make(chan *ToolsCallResult, b.buf)
    b.mu.Lock()
    if b.closed {
        b.mu.Unlock()
        close(ch)
        return &subscription{ch: ch, parent: b}, nil
    }
    b.subs[ch] = struct{}{}
    b.mu.Unlock()
    return &subscription{ch: ch, parent: b}, nil
}

func (b *channelBroadcaster) Publish(ev *ToolsCallResult) {
    if ev == nil {
        return
    }
    b.mu.RLock()
    for ch := range b.subs {
        if b.drop {
            select {
            case ch <- ev:
            default:
            }
        } else {
            ch <- ev
        }
    }
    b.mu.RUnlock()
}

func (b *channelBroadcaster) Close() error {
    b.mu.Lock()
    if b.closed {
        b.mu.Unlock()
        return nil
    }
    b.closed = true
    for ch := range b.subs {
        close(ch)
        delete(b.subs, ch)
    }
    b.mu.Unlock()
    return nil
}

type subscription struct {
    ch     chan *ToolsCallResult
    parent *channelBroadcaster
    once   sync.Once
}

func (s *subscription) C() <-chan *ToolsCallResult {
    return s.ch
}

func (s *subscription) Close() error {
    s.once.Do(func() {
        if s.parent != nil {
            s.parent.mu.Lock()
            delete(s.parent.subs, s.ch)
            s.parent.mu.Unlock()
        }
        close(s.ch)
    })
    return nil
}

{{ comment "Initialize handles the MCP initialize request" }}
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
        ProtocolVersion: a.mcpProtocolVersion(),
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
    a.log(ctx, "request", map[string]any{"method": "ping"})
    res := &PingResult{Pong: true}
    a.log(ctx, "response", map[string]any{"method": "ping"})
    return res, nil
}

{{- if .Tools }}

{{ comment "ToolsList returns the list of available tools" }}
func (a *MCPAdapter) ToolsList(ctx context.Context, p *ToolsListPayload) (*ToolsListResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "tools/list"})
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
    res := &ToolsListResult{Tools: tools}
    a.log(ctx, "response", map[string]any{"method": "tools/list"})
    return res, nil
}

{{ if .ToolsCallStreaming }}
    {{ comment "Stream bridges from original server-streaming methods to MCP ToolsCall stream" }}
    {{- range .Tools }}
        {{- if .IsStreaming }}
type {{ goify .OriginalMethodName }}StreamBridge struct { out ToolsCallServerStream; adapter *MCPAdapter }
func (b *{{ goify .OriginalMethodName }}StreamBridge) Send(ctx context.Context, ev {{ $.Package }}.{{ .StreamEventType }}) error {
    s, serr := encodeJSONToString(ctx, ev)
    if serr != nil {
        return serr
    }
    return b.out.Send(ctx, &ToolsCallResult{
        Content: []*ContentItem{
            buildContentItem(b.adapter, s),
        },
    })
}
func (b *{{ goify .OriginalMethodName }}StreamBridge) SendAndClose(ctx context.Context, ev {{ $.Package }}.{{ .StreamEventType }}) error {
    s, serr := encodeJSONToString(ctx, ev)
    if serr != nil {
        return serr
    }
    return b.out.SendAndClose(ctx, &ToolsCallResult{
        Content: []*ContentItem{
            buildContentItem(b.adapter, s),
        },
    })
}
func (b *{{ goify .OriginalMethodName }}StreamBridge) SendError(ctx context.Context, id string, err error) error {
    return b.out.SendError(ctx, id, err)
}
        {{- end }}
    {{- end }}
{{- end }}

{{ if .ToolsCallStreaming }}
{{ comment "ToolsCall executes a tool and streams progress and final result when requested via SSE" }}
func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload, stream ToolsCallServerStream) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "tools/call", "name": p.Name})
    switch p.Name {
    {{- range .Tools }}
    case {{ quote .Name }}:
        {{- if .HasPayload }}
        // Decode arguments into original payload using goa HTTP decoder
        req := &http.Request{ Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(p.Arguments)) }
        {{- if .IsStreaming }}
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil {
            return goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- if .RequiredFields }}
        // Required fields check (top-level)
        {
            {{- range .RequiredFields }}
            if payload.{{ goify . }} == "" {
                return goa.PermanentError("invalid_params", "Missing required field: {{ . }}")
            }
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
                if !ok && __val != "" {
                    return goa.PermanentError("invalid_params", "Invalid value for {{ $fname }}")
                }
            }
            {{- end }}
        }
        {{- end }}
        // Bridge original server stream interface to MCP ToolsCall stream
        bridge := &{{ goify .OriginalMethodName }}StreamBridge{ out: stream, adapter: a }
        if err := a.service.{{ .OriginalMethodName }}(ctx, payload, bridge); err != nil {
            return a.mapError(err)
        }
        return nil
        {{- else }}
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil {
            return goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- if .RequiredFields }}
        {
            {{- range .RequiredFields }}
            if payload.{{ goify . }} == "" {
                return goa.PermanentError("invalid_params", "Missing required field: {{ . }}")
            }
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
                if !ok && __val != "" {
                    return goa.PermanentError("invalid_params", "Invalid value for {{ $fname }}")
                }
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
        if err != nil {
            return a.mapError(err)
        }
        // Encode result to JSON string using goa encoder
        s, serr := encodeJSONToString(ctx, result)
        if serr != nil {
            return serr
        }
        // Emit final response and close stream
        final := &ToolsCallResult{
            Content: []*ContentItem{
                buildContentItem(a, s),
            },
        }
        a.log(ctx, "response", map[string]any{"method": "tools/call", "name": p.Name})
        return stream.SendAndClose(ctx, final)
        {{- else }}
        {{- if .HasPayload }}
        if err := a.service.{{ .OriginalMethodName }}(ctx, payload); err != nil {
            return a.mapError(err)
        }
        {{- else }}
        if err := a.service.{{ .OriginalMethodName }}(ctx); err != nil {
            return a.mapError(err)
        }
        {{- end }}
        ok := stringPtr("{\"status\":\"success\"}")
        a.log(ctx, "response", map[string]any{"method": "tools/call", "name": p.Name})
        return stream.SendAndClose(ctx, &ToolsCallResult{
            Content: []*ContentItem{
                &ContentItem{
                    Type: "text",
                    Text: ok,
                },
            },
        })
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
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
	switch p.Name {
	{{- range .Tools }}
	case {{ quote .Name }}:
		{{- if .HasPayload }}
		req := &http.Request{ Body: io.NopCloser(bytes.NewReader(p.Arguments)), Header: http.Header{"Content-Type": []string{"application/json"}}, }
		var payload {{ .PayloadType }}
		if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil {
		    return nil, goa.PermanentError("invalid_params", "%s", err.Error())
		}
		{{- end }}
		{{- if .HasResult }}
		{{- if .HasPayload }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
		{{- else }}
		result, err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil {
		    return nil, err
		}
        s, serr := encodeJSONToString(ctx, result)
		if serr != nil {
		    return nil, goa.PermanentError("invalid_params", "%s", serr.Error())
		}
		return &ToolsCallResult{
            Content: []*ContentItem{
                buildContentItem(a, s),
            },
        }, nil
		{{- else }}
		{{- if .HasPayload }}
		if err := a.service.{{ .OriginalMethodName }}(ctx, payload); err != nil {
		    return nil, err
		}
		{{- else }}
		if err := a.service.{{ .OriginalMethodName }}(ctx); err != nil {
		    return nil, err
		}
		{{- end }}
		ok := stringPtr("{\"status\":\"success\"}")
		return &ToolsCallResult{
            Content: []*ContentItem{
                &ContentItem{
                    Type: "text",
                    Text: ok,
                },
            },
        }, nil
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
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "resources/list"})
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
    res := &ResourcesListResult{Resources: resources}
    a.log(ctx, "response", map[string]any{"method": "resources/list"})
    return res, nil
}

{{ comment "ResourcesRead reads a resource and returns its content" }}
func (a *MCPAdapter) ResourcesRead(ctx context.Context, p *ResourcesReadPayload) (*ResourcesReadResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    if err := a.assertResourceURIAllowed(ctx, p.URI); err != nil {
        return nil, goa.PermanentError("invalid_params", "%s", err.Error())
    }
    a.log(ctx, "request", map[string]any{"method": "resources/read", "uri": p.URI})
    baseURI := p.URI
    if i := strings.Index(baseURI, "?"); i >= 0 { baseURI = baseURI[:i] }
    switch baseURI {
{{- range .Resources }}
    case {{ quote .URI }}:
        {{- if .HasPayload }}
        // Map URI query parameters to original payload and decode
        args, aerr := parseQueryParamsToJSON(p.URI)
        if aerr != nil {
            return nil, goa.PermanentError("invalid_params", "%s", aerr.Error())
        }
        req := &http.Request{ Body: io.NopCloser(bytes.NewReader(args)), Header: http.Header{"Content-Type": []string{"application/json"}}, }
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil {
            return nil, goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- end }}
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
        {{- else }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx)
		{{- end }}
		if err != nil {
		    return nil, a.mapError(err)
		}
		s, serr := encodeJSONToString(ctx, result)
		if serr != nil {
		    return nil, goa.PermanentError("invalid_params", "%s", serr.Error())
		}
			res := &ResourcesReadResult{ Contents: []*ResourceContent{ { URI: baseURI, MimeType: stringPtr({{ quote .MimeType }}), Text: &s } } }
			a.log(ctx, "response", map[string]any{"method": "resources/read", "uri": baseURI})
			return res, nil
		{{- else }}
		{{- if .HasPayload }}
		if err := a.service.{{ .OriginalMethodName }}(ctx, payload); err != nil {
		    return nil, a.mapError(err)
		}
		{{- else }}
		if err := a.service.{{ .OriginalMethodName }}(ctx); err != nil {
		    return nil, a.mapError(err)
		}
		{{- end }}
        res := &ResourcesReadResult{ Contents: []*ResourceContent{ { URI: baseURI, MimeType: stringPtr({{ quote .MimeType }}), Text: stringPtr("{\"status\":\"success\"}") } } }
        a.log(ctx, "response", map[string]any{"method": "resources/read", "uri": baseURI})
        return res, nil
		{{- end }}
	{{- end }}
    default:
        return nil, goa.PermanentError("method_not_found", "Unknown resource: %s", p.URI)
	}
}

// assertResourceURIAllowed verifies pURI passes allow/deny filters when configured.
func (a *MCPAdapter) assertResourceURIAllowed(ctx context.Context, pURI string) error {
    if a == nil || a.opts == nil {
        // Allow header-driven policy even if opts is nil
    }
    base := pURI
    if i := strings.Index(base, "?"); i >= 0 { base = base[:i] }
    // Build name->URI map from generated resources
    nameToURI := map[string]string{
        {{- range .Resources }}
        {{ printf "%q" .Name }}: {{ printf "%q" .URI }},
        {{- end }}
    }
    // Merge header-driven allow/deny lists from context (CSV of names)
    var extraAllowURIs, extraDenyURIs []string
    if ctx != nil {
        if v := ctx.Value("mcp_allow_names"); v != nil {
            if s, ok := v.(string); ok {
                for _, n := range strings.Split(s, ",") {
                    n = strings.TrimSpace(n)
                    if u, ok2 := nameToURI[n]; ok2 { extraAllowURIs = append(extraAllowURIs, u) }
                }
            }
        }
        if v := ctx.Value("mcp_deny_names"); v != nil {
            if s, ok := v.(string); ok {
                for _, n := range strings.Split(s, ",") {
                    n = strings.TrimSpace(n)
                    if u, ok2 := nameToURI[n]; ok2 { extraDenyURIs = append(extraDenyURIs, u) }
                }
            }
        }
    }
    // If header-based allow explicitly lists this URI, allow it (overrides default denies)
    for _, allow := range extraAllowURIs {
        if allow == base {
            return nil
        }
    }
    // Otherwise, deny list takes precedence
    for _, d := range append(a.opts.DeniedResourceURIs, extraDenyURIs...) {
        if d == base {
            return fmt.Errorf("resource URI denied: %s", pURI)
        }
    }
    // If any allow URIs are set (via opts or headers), require membership; otherwise allow all
    if len(a.opts.AllowedResourceURIs) == 0 && len(extraAllowURIs) == 0 {
        return nil
    }
    for _, allow := range append(a.opts.AllowedResourceURIs, extraAllowURIs...) {
        if allow == base {
            return nil
        }
    }
    return fmt.Errorf("resource URI not allowed: %s", pURI)
}

{{ comment "ResourcesSubscribe subscribes to resource changes" }}
func (a *MCPAdapter) ResourcesSubscribe(ctx context.Context, p *ResourcesSubscribePayload) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    // Subscriptions are supported only when explicitly marked watchable in the DSL
    // (explicit opt-in). Unknown URIs yield method_not_found to avoid false-positive success.
    switch p.URI {
    {{- range .Resources }}
    {{- if .Watchable }}
    case {{ quote .URI }}:
        a.subsMu.Lock()
        a.subs[p.URI] = a.subs[p.URI] + 1
        a.subsMu.Unlock()
        return nil
    {{- end }}
    {{- end }}
    default:
        return goa.PermanentError("method_not_found", "Unknown resource: %s", p.URI)
    }
}

{{ comment "ResourcesUnsubscribe unsubscribes from resource changes" }}
func (a *MCPAdapter) ResourcesUnsubscribe(ctx context.Context, p *ResourcesUnsubscribePayload) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    // Unsubscribe succeeds only for URIs declared watchable (explicit opt-in);
    // otherwise report method_not_found.
    switch p.URI {
    {{- range .Resources }}
    {{- if .Watchable }}
    case {{ quote .URI }}:
        a.subsMu.Lock()
        if n, ok := a.subs[p.URI]; ok {
            if n > 1 { a.subs[p.URI] = n - 1 } else { delete(a.subs, p.URI) }
        }
        a.subsMu.Unlock()
        return nil
    {{- end }}
    {{- end }}
    default:
        return goa.PermanentError("method_not_found", "Unknown resource: %s", p.URI)
    }
}
{{- end }}

{{- if or .StaticPrompts .DynamicPrompts }}

{{ comment "PromptsList returns the list of available prompts" }}
func (a *MCPAdapter) PromptsList(ctx context.Context, p *PromptsListPayload) (*PromptsListResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "prompts/list"})
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
    res := &PromptsListResult{Prompts: prompts}
    a.log(ctx, "response", map[string]any{"method": "prompts/list"})
    return res, nil
}

{{ comment "PromptsGet resolves static prompts directly and delegates dynamic prompts to the provider" }}
func (a *MCPAdapter) PromptsGet(ctx context.Context, p *PromptsGetPayload) (*PromptsGetResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    if p == nil || p.Name == "" {
        return nil, goa.PermanentError("invalid_params", "Missing prompt name")
    }
    a.log(ctx, "request", map[string]any{"method": "prompts/get", "name": p.Name})

    // Static prompts handled inline with optional provider override
    switch p.Name {
    {{- range .StaticPrompts }}
    case "{{ .Name }}":
        if a.promptProvider != nil {
            if res, err := a.promptProvider.Get{{ goify .Name }}Prompt(p.Arguments); err == nil && res != nil {
                a.log(ctx, "response", map[string]any{"method": "prompts/get", "name": p.Name})
                return res, nil
            } else if err != nil {
                return nil, err
            }
        }
        // Fallback to generated static prompt content
        msgs := make([]*PromptMessage, 0, {{ len .Messages }})
        {{- range .Messages }}
        msgs = append(msgs, &PromptMessage{ Role: {{ quote .Role }}, Content: &MessageContent{ Type: "text", Text: stringPtr({{ quote .Content }}) } })
        {{- end }}
        res := &PromptsGetResult{ Description: stringPtr({{ quote .Description }}), Messages: msgs }
        a.log(ctx, "response", map[string]any{"method": "prompts/get", "name": p.Name})
        return res, nil
    {{- end }}
    }

    // Dynamic prompts: only require provider when the requested name matches
    {{- if .DynamicPrompts }}
    switch p.Name {
    {{- range .DynamicPrompts }}
    case "{{ .Name }}":
        // Validate required arguments for dynamic prompt {{ .Name }}
        {
        {{- $hasRequired := false }}
        {{- range .Arguments }}
            {{- if .Required }}
                {{- $hasRequired = true }}
            {{- end }}
        {{- end }}
        {{- if $hasRequired }}
            var args map[string]any
            if len(p.Arguments) > 0 {
                if err := json.Unmarshal(p.Arguments, &args); err != nil {
                    return nil, goa.PermanentError("invalid_params", "%s", err.Error())
                }
            } 
            {{- range .Arguments }}
                {{- if .Required }}
            if _, ok := args["{{ .Name }}"]; !ok {
                return nil, goa.PermanentError("invalid_params", "Missing required argument: {{ .Name }}")
            } 
                {{- end }}
            {{- end }}
        {{- end }}
        }
        if a.promptProvider == nil {
            return nil, goa.PermanentError("invalid_params", "No prompt provider configured for dynamic prompts")
        }
        res, err := a.promptProvider.Get{{ goify .Name }}Prompt(ctx, p.Arguments)
        if err != nil {
            return nil, a.mapError(err)
        }
        a.log(ctx, "response", map[string]any{"method": "prompts/get", "name": p.Name})
        return res, nil
    {{- end }}
    }
    {{- end }}

    return nil, goa.PermanentError("method_not_found", "Unknown prompt: %s", p.Name)
}
{{- end }}

{{- if .Notifications }}
{{ comment "NotifyStatusUpdate handles notifications with no response" }}
func (a *MCPAdapter) NotifyStatusUpdate(ctx context.Context, n *mcpruntime.Notification) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    if n == nil || n.Type == "" {
        return goa.PermanentError("invalid_params", "Missing notification type")
    }
    s, err := mcpruntime.EncodeJSONToString(ctx, goahttp.ResponseEncoder, n)
    if err != nil {
        return err
    }
    ev := &ToolsCallResult{
        Content: []*ContentItem{
            buildContentItem(a, s),
        },
    }
    a.Publish(ev)
    return nil
}

{{ comment "EventsStream streams server notifications over SSE" }}
func (a *MCPAdapter) EventsStream(ctx context.Context, stream ToolsCallServerStream) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    if a.broadcaster == nil {
        return goa.PermanentError("invalid_params", "No broadcaster configured")
    }
    sub, _ := a.broadcaster.Subscribe(ctx)
    defer sub.Close()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case ev, ok := <-sub.C():
            if !ok {
                return nil
            }
            if err := stream.Send(ctx, ev); err != nil {
                return err
            }
        }
    }
}
{{- end }}

{{- if .Subscriptions }}
{{ comment "Subscribe returns a default success for demo purposes" }}
func (a *MCPAdapter) Subscribe(ctx context.Context, p *SubscribePayload) (*SubscribeResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "subscribe"})
    res := &SubscribeResult{Success: true}
    a.log(ctx, "response", map[string]any{"method": "subscribe"})
    return res, nil
}

{{ comment "Unsubscribe returns a default success for demo purposes" }}
func (a *MCPAdapter) Unsubscribe(ctx context.Context, p *UnsubscribePayload) (*UnsubscribeResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "unsubscribe"})
    res := &UnsubscribeResult{Success: true}
    a.log(ctx, "response", map[string]any{"method": "unsubscribe"})
    return res, nil
}
{{- end }}

{{ comment "stringPtr returns a pointer to a string" }}
func stringPtr(s string) *string {
	return &s
}

// buildContentItem returns a ContentItem honoring StructuredStreamJSON option.
func buildContentItem(a *MCPAdapter, s string) *ContentItem {
    if a != nil && a.opts != nil && a.opts.StructuredStreamJSON && isLikelyJSON(s) {
        mt := stringPtr("application/json")
        return &ContentItem{ Type: "text", MimeType: mt, Text: &s }
    }
    return &ContentItem{ Type: "text", Text: &s }
}

func isLikelyJSON(s string) bool {
    return json.Valid([]byte(s))
}

// mapError and log helpers (no-op if options are nil)
func (a *MCPAdapter) mapError(err error) error {
    if a != nil && a.opts != nil && a.opts.ErrorMapper != nil && err != nil {
        if m := a.opts.ErrorMapper(err); m != nil {
            return m
        }
    }
    return err
}

func (a *MCPAdapter) log(ctx context.Context, event string, details any) {
    if a != nil && a.opts != nil && a.opts.Logger != nil {
        a.opts.Logger(ctx, event, details)
    }
}

// Publish sends an event to all event stream subscribers.
func (a *MCPAdapter) Publish(ev *ToolsCallResult) {
    if a == nil || a.broadcaster == nil {
        return
    }
    a.broadcaster.Publish(ev)
}

// PublishStatus is a convenience to publish a status_update message.
func (a *MCPAdapter) PublishStatus(ctx context.Context, typ string, message string, data any) {
    m := map[string]any{"type": typ, "message": message}
    if data != nil { m["data"] = data }
    s, err := encodeJSONToString(ctx, m)
    if err != nil {
        return
    }
    a.Publish(&ToolsCallResult{
        Content: []*ContentItem{
            buildContentItem(a, s),
        },
    })
}
