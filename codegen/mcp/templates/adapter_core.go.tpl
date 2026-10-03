// MCPAdapter calls the authored Goa service after the HTTP binding and generated
// argument codecs accept the request. It returns only this release's MCP contract.
type (
    // MCPAdapter translates protocol requests into authored service operations.
    MCPAdapter struct {
        service {{ .Package }}.Service
        opts MCPAdapterOptions
    }
    // MCPAdapterOptions configures how application errors are exposed to clients.
    MCPAdapterOptions struct {
        // ErrorMapper replaces a service error with an application-approved error.
        // It must return a non-nil error; returning nil violates this contract.
        ErrorMapper func(error) error
    }
)

// NewMCPAdapter connects an already-built service to the MCP protocol methods.
func NewMCPAdapter(service {{ .Package }}.Service, opts *MCPAdapterOptions) *MCPAdapter {
    adapter := &MCPAdapter{service: service}
    if opts != nil {
        adapter.opts = *opts
    }
    return adapter
}

{{- if .NeedsNoArgumentsValidation }}
// validateNoArguments accepts omitted arguments or an empty JSON object for a
// method with no payload. Any supplied property is an argument error.
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

// mapError applies the host's error disclosure policy to a service failure.
func (a *MCPAdapter) mapError(err error) error {
    if a.opts.ErrorMapper != nil {
        return a.opts.ErrorMapper(err)
    }
    return err
}

func stringPtr(value string) *string {
    return &value
}
{{- if .NeedsBoolPtr }}
func boolPtr(value bool) *bool {
    return &value
}
{{- end }}

// resultMeta identifies the server software on each independent response.
func resultMeta() json.RawMessage {
    return json.RawMessage({{ quote .ResultMeta }})
}

// ServerDiscover describes declared capabilities without creating client state.
func (a *MCPAdapter) ServerDiscover(ctx context.Context, _ *DiscoverPayload) (*DiscoverResult, error) {
    _, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.server/discover")
    defer span.End()
    capabilities := &ServerCapabilities{}
    {{- if .Tools }}
    capabilities.Tools = &ToolsCapability{}
    {{- end }}
    {{- if .Resources }}
    capabilities.Resources = &ResourcesCapability{}
    {{- end }}
    {{- if or .StaticPrompts .MethodPrompts }}
    capabilities.Prompts = &PromptsCapability{}
    {{- end }}
    {{- if .PromptCompletions }}
    capabilities.Completions = &CompletionsCapability{}
    {{- end }}
    return &DiscoverResult{
        ResultType: "complete",
        Meta: resultMeta(),
        SupportedVersions: []string{mcpruntime.ProtocolVersion},
        Capabilities: capabilities,
        TTLMs: 0,
        CacheScope: "private",
    }, nil
}
