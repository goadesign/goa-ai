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
        // It controls the disclosed message, while the original error determines
        // whether the failure is internal. It must return a non-nil error.
        ErrorMapper func(error) error
    }
    // endpointFailure keeps the disclosed error and its original server-fault
    // classification together. Redacting a message cannot change who caused it.
    endpointFailure struct {
        err error
        internal bool
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

// mapError applies the host's disclosure policy after classifying the original
// endpoint error. Internal failures remain protocol errors after redaction.
func (a *MCPAdapter) mapError(err error, internal bool) endpointFailure {
    {{- if .NeedsEndpointResultCheck }}
    if failure, ok := err.(*endpointResultError); ok {
        return endpointFailure{err: goa.PermanentError("internal_error", "%s", failure.Error()), internal: true}
    }
    {{- end }}
    disclosed := err
    if a.opts.ErrorMapper != nil {
        disclosed = a.opts.ErrorMapper(err)
    }
    if internal {
        disclosed = goa.NewServiceError(disclosed, "internal_error", false, false, true)
    }
    return endpointFailure{err: disclosed, internal: internal}
}

{{- if .EndpointMethods }}
// endpointErrorOwner follows one wrapped failure to its first named Goa error.
// A join of independent errors keeps its own meaning instead of borrowing one
// child's fault flag or declared name.
func endpointErrorOwner(err error) error {
    for err != nil {
        if _, named := err.(goa.GoaErrorNamer); named {
            return err
        }
        if joined, ok := err.(interface{ Unwrap() []error }); ok {
            causes := joined.Unwrap()
            if len(causes) != 1 {
                return err
            }
            err = causes[0]
        } else {
            cause := errors.Unwrap(err)
            if cause == nil {
                return err
            }
            err = cause
        }
    }
    return nil
}

{{- $needsGenericFault := false }}
{{- range .EndpointMethods }}
{{- if not .FaultNames }}{{- $needsGenericFault = true }}{{- end }}
{{- end }}
{{- if $needsGenericFault }}
// isEndpointFault recognizes the owning Goa error's explicit server-fault flag.
func isEndpointFault(err error) bool {
    failure, ok := endpointErrorOwner(err).(*goa.ServiceError)
    return ok && failure.Fault
}
{{- end }}
{{- range .EndpointMethods }}
{{- if .FaultNames }}
// is{{ .CallName }}Fault also recognizes this method's authored fault errors.
func is{{ .CallName }}Fault(err error) bool {
    owner := endpointErrorOwner(err)
    if failure, ok := owner.(*goa.ServiceError); ok && failure.Fault {
        return true
    }
    if named, ok := owner.(goa.GoaErrorNamer); ok {
        switch named.GoaErrorName() {
        case {{ range $index, $name := .FaultNames }}{{ if $index }}, {{ end }}{{ quote $name }}{{ end }}:
            return true
        }
    }
    return false
}
{{- end }}
{{- end }}
{{- end }}

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
func (a *MCPAdapter) ServerDiscover(ctx context.Context, _ {{ index .PayloadRefs "server/discover" }}) (*DiscoverResult, error) {
    _, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.server/discover")
    defer span.End()
    capabilities := &ServerCapabilities{}
    {{- if .Tasks }}
    capabilities.Extensions = json.RawMessage(`{"io.modelcontextprotocol/tasks":{}}`)
    {{- end }}
    {{- if .Tools }}
    capabilities.Tools = &ToolsCapability{ {{ if and .SubscriptionSource (index .SubscriptionSource.Catalogs "tools") }}ListChanged: boolPtr(true),{{ end }} }
    {{- end }}
    {{- if or .Resources .ResourceTemplates }}
    capabilities.Resources = &ResourcesCapability{ {{ if and .SubscriptionSource .SubscriptionSource.Resources }}Subscribe: boolPtr(true),{{ end }} }
    {{- end }}
    {{- if or .StaticPrompts .MethodPrompts }}
    capabilities.Prompts = &PromptsCapability{ {{ if and .SubscriptionSource (index .SubscriptionSource.Catalogs "prompts") }}ListChanged: boolPtr(true),{{ end }} }
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
{{- if not .Streaming }}
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
    return {{ if .ProjectedResult }}result.Projected{{ else }}result{{ end }}, nil
    {{- else }}
    return err
    {{- end }}
}
{{ end }}

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

{{- range .EndpointMethods }}
{{- $endpoint := . }}
{{- if and .ExecutionView (not .TaskCreator) }}
{{- with .Codec }}
{{- if .ResultEncode }}
// {{ .ResultEncode }} encodes the endpoint's selected view and its exact fields.
// The declared OneOf tag keeps that choice visible to decoders and saved results.
func {{ .ResultEncode }}(result {{ $endpoint.EndpointResultRef }}) ([]byte, error) {
    {{- if $endpoint.InputExchange }}
    completedResult, _ := {{ $endpoint.InputExchange.OutcomeValue }}.AsComplete()
    {{- end }}
    switch result.View {
    {{- range .ResultViews }}
    case {{ quote .Name }}:
        encoded, err := {{ $.CodecPackage }}.{{ .Encode }}({{ if $endpoint.InputExchange }}completedResult{{ else }}result.Projected{{ end }})
        if err != nil {
            return nil, err
        }
        return json.Marshal(struct {
            Type string `json:"type"`
            Value json.RawMessage `json:"value"`
        }{Type: {{ quote .Name }}, Value: json.RawMessage(encoded)})
    {{- end }}
    default:
        return nil, fmt.Errorf("endpoint returned undeclared result view %q", result.View)
    }
}
{{- end }}
{{- if .ResultValidate }}
// {{ .ResultValidate }} checks the fields of the endpoint's selected view
// before a prompt, resource or suggestion is copied into its MCP response.
func {{ .ResultValidate }}(result {{ $endpoint.EndpointResultRef }}) error {
    {{- if $endpoint.InputExchange }}
    completedResult, _ := {{ $endpoint.InputExchange.OutcomeValue }}.AsComplete()
    {{- end }}
    switch result.View {
    {{- range .ResultViews }}
    case {{ quote .Name }}:
        return {{ $.CodecPackage }}.{{ .Validate }}({{ if $endpoint.InputExchange }}completedResult{{ else }}result.Projected{{ end }})
    {{- end }}
    default:
        return fmt.Errorf("endpoint returned undeclared result view %q", result.View)
    }
}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{- range .EndpointMethods }}
{{- if or .Credentials .Paths .InputExchange }}
// fill{{ .CallName }}Inputs places transport values and typed host answers in
// the original service payload. Full validation runs before endpoint dispatch;
// invalid credentials return a fixed error without disclosing their values.
func fill{{ .CallName }}Inputs(payload {{ .PayloadRef }}{{ range $index, $field := .Credentials }}, credential{{ $index }} *string{{ end }}{{ range .Paths }}, {{ .ValueName }}Raw string{{ end }}{{ if .InputExchange }}, requestState *string, inputResponses map[string]json.RawMessage{{ end }}) error {
    {{- if .Paths }}
    var err error
    {{- range .Paths }}
    var {{ .ValueName }} {{ .ValueRef }}
    {
        {{ .Decode }}
    }
    {{- end }}
    if err != nil {
        return goa.PermanentError("invalid_params", "%s", err.Error())
    }
    {{- range .Paths }}
    {{ .Conversion }}
    {{- end }}
    {{- end }}
    {{- range $index, $field := .Credentials }}
    {{- if .Required }}
    if credential{{ $index }} == nil {
        return goa.PermanentError("invalid_params", "missing required HTTP credential")
    }
    {{- end }}
    {{- if .AlternativeScheme }}
    if credential{{ $index }} != nil && !strings.EqualFold(strings.SplitN(*credential{{ $index }}, " ", 2)[0], {{ quote .AlternativeScheme }}) {
    {{- else }}
    if credential{{ $index }} != nil {
    {{- end }}
        {{- if .Basic }}
        request := &http.Request{Header: http.Header{"Authorization": []string{*credential{{ $index }}}}}
        {{ if .Username }}username, _{{ else }}_, password{{ end }}, valid := request.BasicAuth()
        if !valid {
            return goa.PermanentError("invalid_params", "invalid HTTP Basic credential")
        }
        {{- if .Username }}
        value := {{ .TypeRef }}(username)
        {{- else }}
        value := {{ .TypeRef }}(password)
        {{- end }}
        {{- else if .Bearer }}
        scheme, token, present := strings.Cut(*credential{{ $index }}, " ")
        token = strings.TrimLeft(token, " ")
        if !present || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
            return goa.PermanentError("invalid_params", "invalid HTTP Bearer credential")
        }
        value := {{ .TypeRef }}(token)
        {{- else }}
        value := {{ .TypeRef }}(*credential{{ $index }})
        {{- end }}
        payload.{{ .Target }} = {{ if .Pointer }}&{{ end }}value
    }
    {{- end }}
	{{- with .InputExchange }}
    {{ .ContinuationSource }}
    {{- end }}
    if err := {{ .InputValidate }}(payload); err != nil {
        return goa.PermanentError("invalid_params", "{{ if .InputExchange }}Operation inputs{{ else }}HTTP {{ if .Paths }}inputs{{ else }}credentials{{ end }}{{ end }} do not match the service contract")
    }
    return nil
}
{{- range .RouteHelpers }}
// {{ .Name }} converts decoded URL values into the authored service type.
func {{ .Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
    {{ .Code }}
}
{{- end }}
{{- end }}
{{- end }}

{{- range .EndpointMethods }}
{{- $endpoint := . }}
{{- with .InputExchange }}
// convert{{ $endpoint.CallName }}Pending validates the service's unfinished
// result and returns its selected questions and exact state. The current
// request's capabilities must support every selected form or URL interaction.
func convert{{ $endpoint.CallName }}Pending(pending {{ .PendingRef }}, meta json.RawMessage) (*InputRequiredResult, error) {
    {{ .PendingSource }}
}
{{- end }}
{{- end }}

{{- $hasQuestions := false }}
{{- range .EndpointMethods }}
{{- with .InputExchange }}
{{- if .Questions }}{{- $hasQuestions = true }}{{- end }}
{{- end }}
{{- end }}
{{- range .Tasks }}
{{- if .Input.Questions }}{{- $hasQuestions = true }}{{- end }}
{{- end }}
{{- if $hasQuestions }}
// validateInputCapabilities reads support advertised on this request and returns
// the native MCP capability error when a selected question cannot be answered.
func validateInputCapabilities(meta json.RawMessage, required *RequiredClientCapabilities) error {
    var advertised struct {
        Capabilities *struct {
            Elicitation json.RawMessage `json:"elicitation"`
        } `json:"io.modelcontextprotocol/clientCapabilities"`
    }
    if err := json.Unmarshal(meta, &advertised); err != nil {
        return goa.PermanentError("invalid_params", "invalid clientCapabilities: %s", err.Error())
    }
    if advertised.Capabilities == nil || len(advertised.Capabilities.Elicitation) == 0 {
        return &MissingClientCapabilityError{RequiredCapabilities: required}
    }
    var modes map[string]json.RawMessage
    if err := json.Unmarshal(advertised.Capabilities.Elicitation, &modes); err != nil || modes == nil {
        return goa.PermanentError("invalid_params", "elicitation capability must be an object")
    }
    var form, url *struct{}
    if raw, present := modes["form"]; present {
        if err := json.Unmarshal(raw, &form); err != nil || form == nil {
            return goa.PermanentError("invalid_params", "elicitation form capability must be an object")
        }
    }
    if raw, present := modes["url"]; present {
        if err := json.Unmarshal(raw, &url); err != nil || url == nil {
            return goa.PermanentError("invalid_params", "elicitation URL capability must be an object")
        }
    }
    // The current protocol defines an empty elicitation object as form-only
    // support. Explicit modes still govern every nonempty capability object.
    supportsForm := len(modes) == 0 || form != nil
    if (required.Elicitation.Form != nil && !supportsForm) ||
        (required.Elicitation.URL != nil && url == nil) {
        return &MissingClientCapabilityError{RequiredCapabilities: required}
    }
    return nil
}
{{- end }}
