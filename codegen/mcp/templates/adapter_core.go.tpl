// MCPAdapter calls the configured Goa endpoints after the HTTP binding and
// argument codecs accept the request. Endpoint authentication and middleware
// retain their existing owner; results use only this release's MCP contract.
type (
    // MCPAdapter translates protocol requests into authored service operations.
    MCPAdapter struct {
        endpoints *{{ .Package }}.{{ .EndpointsName }}
        opts MCPAdapterOptions
    }
    // MCPAdapterOptions configures how application errors are exposed to clients.
    MCPAdapterOptions struct {
        // ErrorMapper replaces a service error with an application-approved error.
        // It must return a non-nil error; returning nil violates this contract.
        ErrorMapper func(error) error
    }
)

// NewMCPAdapter connects already-configured Goa endpoints to MCP methods.
// Configure authentication, interceptors and middleware before constructing it.
func NewMCPAdapter(endpoints *{{ .Package }}.{{ .EndpointsName }}, opts *MCPAdapterOptions) *MCPAdapter {
    adapter := &MCPAdapter{endpoints: endpoints}
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

// mapError turns invalid endpoint result types into internal protocol errors
// and applies the host's disclosure policy to application failures.
func (a *MCPAdapter) mapError(err error) error {
    {{- if .NeedsEndpointResultCheck }}
    if failure, ok := err.(*endpointResultError); ok {
        return goa.PermanentError("internal_error", "%s", failure.Error())
    }
    {{- end }}
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
    {{- if or .Resources .ResourceTemplates }}
    capabilities.Resources = &ResourcesCapability{}
    {{- end }}
    {{- if or .StaticPrompts .MethodPrompts }}
    capabilities.Prompts = &PromptsCapability{}
    {{- end }}
    {{- if .Completions }}
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

{{ range .EndpointMethods }}
// {{ .CallName }} sends validated input to the configured {{ .MethodName }} endpoint
// and returns its declared result. An unexpected Go type is an internal error.
func (a *MCPAdapter) {{ .CallName }}(ctx context.Context{{ if .PayloadRef }}, payload {{ .PayloadRef }}{{ end }}) ({{ if .ResultRef }}{{ .ResultRef }}, {{ end }}error) {
    // The original endpoint middleware observes its authored service and method.
    ctx = context.WithValue(ctx, goa.ServiceKey, {{ quote $.ServiceName }})
    ctx = context.WithValue(ctx, goa.MethodKey, {{ quote .DesignMethodName }})
    {{ if .ResultRef }}raw{{ else }}_{{ end }}, err := a.endpoints.{{ .MethodName }}(ctx, {{ if .PayloadRef }}payload{{ else }}nil{{ end }})
    {{- if .ResultRef }}
    var zero {{ .ResultRef }}
    if err != nil {
        return zero, err
    }
    result, ok := raw.({{ .EndpointResultRef }})
    if !ok {
        return zero, &endpointResultError{method: {{ quote .MethodName }}}
    }
    return {{ if .ProjectedResult }}result.Projected{{ else if .ResultConstructor }}{{ .ResultConstructor }}(result){{ else }}result{{ end }}, nil
    {{- else }}
    return err
    {{- end }}
}
{{ end }}

{{- if .NeedsEndpointResultCheck }}
// endpointResultError identifies a configured endpoint that returned a value
// outside its declared Go contract. Applications cannot remap this invariant error.
type endpointResultError struct {
    method string
}

// Error identifies the endpoint whose result failed the generated type check.
func (e *endpointResultError) Error() string {
    return fmt.Sprintf("endpoint %s returned an unexpected Go result type", e.method)
}
{{- end }}
