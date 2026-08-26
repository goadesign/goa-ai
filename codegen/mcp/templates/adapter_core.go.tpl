{{ comment "MCPAdapter core: types, options, constructor, helpers" }}

type (
    // MCPAdapter translates MCP protocol requests into calls to the authored
    // Goa service.
    MCPAdapter struct {
        service {{ .Package }}.Service
        opts *MCPAdapterOptions
    }

    // MCPAdapterOptions allows customizing adapter behavior.
    MCPAdapterOptions struct {
        // Logger is an optional hook called with internal adapter events.
        Logger func(ctx context.Context, event string, details any)
        // ErrorMapper replaces a service error before the MCP client sees it.
        ErrorMapper func(error) error
    }
)

// NewMCPAdapter returns an MCP service that calls the Goa service implementation.
func NewMCPAdapter(service {{ .Package }}.Service, opts *MCPAdapterOptions) *MCPAdapter {
    return &MCPAdapter{
        service: service,
        opts: opts,
    }
}

{{- if .NeedsNoArgumentsValidation }}
// validateNoArguments accepts omitted arguments and an empty JSON object.
// It rejects all data because the selected Goa method accepts no input.
func validateNoArguments(arguments json.RawMessage) error {
    if len(arguments) == 0 {
        return nil
    }
    var fields map[string]json.RawMessage
    if err := json.Unmarshal(arguments, &fields); err != nil {
        return fmt.Errorf("arguments must be an empty JSON object: %w", err)
    }
    if fields == nil || len(fields) > 0 {
        return fmt.Errorf("arguments must be an empty JSON object")
    }
    return nil
}
{{- end }}

func (a *MCPAdapter) log(ctx context.Context, event string, details any) {
    if a != nil && a.opts != nil && a.opts.Logger != nil {
        a.opts.Logger(ctx, event, details)
    }
}

// mapError lets the application replace a service error before it is returned.
func (a *MCPAdapter) mapError(err error) error {
    if a != nil && a.opts != nil && a.opts.ErrorMapper != nil && err != nil {
        if m := a.opts.ErrorMapper(err); m != nil {
            return m
        }
    }
    return err
}

func stringPtr(s string) *string {
    return &s
}

{{- if .NeedsBoolPtr }}
func boolPtr(value bool) *bool {
    return &value
}
{{- end }}

// Initialize handles the MCP initialize request.
func (a *MCPAdapter) Initialize(_ context.Context, _ *InitializePayload) (*InitializeResult, error) {
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
    {{- if .StaticPrompts }}
    capabilities.Prompts = &PromptsCapability{}
    {{- end }}

    return &InitializeResult{
        ProtocolVersion: DefaultProtocolVersion,
        ServerInfo:      serverInfo,
        Capabilities:    capabilities,
    }, nil
}

// NotificationsInitialized accepts the notification sent after initialization.
// The HTTP transport tracks setup separately for each client session.
func (a *MCPAdapter) NotificationsInitialized(_ context.Context, _ *InitializedPayload) error {
    return nil
}

// Ping handles the MCP ping request.
func (a *MCPAdapter) Ping(ctx context.Context) (*PingResult, error) {
    a.log(ctx, "request", map[string]any{"method": "ping"})
    res := &PingResult{}
    a.log(ctx, "response", map[string]any{"method": "ping"})
    return res, nil
}
