// This file calls the Goa service methods used by {{ .Toolset.Name }} tools.
// It decodes each tool request, fills server-owned fields, calls the service,
// and returns the service result as a tool result.
//
// Example:
//
//   client := atlasdata.NewClient(...)
//   exec := {{ .Toolset.PackageName }}.{{ .Constructor }}({{ .Toolset.PackageName }}.{{ .Names.WithClient }}(client))

type (
    {{ .Names.ConfigType }} struct {
        {{- range .Tools }}
        {{- if .IsMethodBacked }}
        {{ .CallerField }} func(context.Context, any) (any, error)
        {{- end }}
        {{- end }}
        mapPayload func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)
        mapResult  func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)
        injectors  []{{ .Names.InterceptorType }}
    }
    // {{ .Names.OptionType }} changes how the generated executor calls the service.
    {{ .Names.OptionType }} interface{ apply(*{{ .Names.ConfigType }}) }

    // {{ .Names.InterceptorType }} can fill service input fields before a call.
    {{ .Names.InterceptorType }} interface {
        // Inject receives the service input and information about the tool call.
        Inject(ctx context.Context, payload any, meta *runtime.ToolCallMeta) error
    }

    // {{ .Names.InterceptorFuncType }} lets a function fill service input fields.
    {{ .Names.InterceptorFuncType }} func(context.Context, any, *runtime.ToolCallMeta) error
)

// Inject calls f with the service input and tool call information.
func (f {{ .Names.InterceptorFuncType }}) Inject(ctx context.Context, p any, m *runtime.ToolCallMeta) error {
    return f(ctx, p, m)
}

type {{ .Names.OptionFuncType }} func(*{{ .Names.ConfigType }})

func (f {{ .Names.OptionFuncType }}) apply(c *{{ .Names.ConfigType }}) { f(c) }

// {{ .Names.WithPayloadMapper }} sets the function that converts tool input into service input.
func {{ .Names.WithPayloadMapper }}(f func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)) {{ .Names.OptionType }} {
    return {{ .Names.OptionFuncType }}(func(c *{{ .Names.ConfigType }}) { c.mapPayload = f })
}

// {{ .Names.WithResultMapper }} sets the function that converts a service result into a tool result.
func {{ .Names.WithResultMapper }}(f func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)) {{ .Names.OptionType }} {
    return {{ .Names.OptionFuncType }}(func(c *{{ .Names.ConfigType }}) { c.mapResult = f })
}

// {{ .Names.WithInterceptors }} adds functions that fill service input fields before each call.
func {{ .Names.WithInterceptors }}(interceptors ...{{ .Names.InterceptorType }}) {{ .Names.OptionType }} {
    return {{ .Names.OptionFuncType }}(func(c *{{ .Names.ConfigType }}) {
        c.injectors = append(c.injectors, interceptors...)
    })
}

// {{ .Names.WithClient }} calls each tool's Goa method through client.
// Callers can replace an individual method with its generated With<Tool> option.
func {{ .Names.WithClient }}(client *{{ .ServiceClientRef }}) {{ .Names.OptionType }} {
    return {{ .Names.OptionFuncType }}(func(c *{{ .Names.ConfigType }}) {
        if client == nil {
            return
        }
        {{- range .Tools }}
        {{- if .IsMethodBacked }}
        c.{{ .CallerField }} = func(ctx context.Context, args any) (any, error) {
            {{- if .HasResult }}
                {{- if .MethodPayloadTypeRef }}
            return client.{{ .MethodGoName }}(ctx, args.({{ .MethodPayloadTypeRef }}))
                {{- else }}
            return client.{{ .MethodGoName }}(ctx)
                {{- end }}
            {{- else }}
                {{- if .MethodPayloadTypeRef }}
            err := client.{{ .MethodGoName }}(ctx, args.({{ .MethodPayloadTypeRef }}))
                {{- else }}
            err := client.{{ .MethodGoName }}(ctx)
                {{- end }}
            return nil, err
            {{- end }}
        }
        {{- end }}
        {{- end }}
    })
}

{{- range .Tools }}
{{- if .IsMethodBacked }}
// {{ .HelperCallerOption }} sets the caller for {{ .QualifiedName }}.
func {{ .HelperCallerOption }}(f func(context.Context, any) (any, error)) {{ $.Names.OptionType }} {
    return {{ $.Names.OptionFuncType }}(func(c *{{ $.Names.ConfigType }}) {
        c.{{ .CallerField }} = f
    })
}
{{- end }}
{{- end }}
// {{ .Constructor }} returns an executor for the Goa methods used by this toolset.
func {{ .Constructor }}(opts ...{{ .Names.OptionType }}) runtime.ToolCallExecutor {
    var cfg {{ .Names.ConfigType }}

    for _, o := range opts {
        if o != nil {
            o.apply(&cfg)
        }
    }
    {{- range .Tools }}
    {{- if .IsMethodBacked }}
    if cfg.{{ .CallerField }} == nil {
        panic(fmt.Errorf("service executor missing caller for tool %q", {{ printf "%q" .QualifiedName }}))
    }
    {{- end }}
    {{- end }}
    return runtime.ToolCallExecutorFunc(func(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
        if call == nil {
            return runtime.Executed({{ .Names.FailedToolResult }}("", errors.New("tool request is nil"))), nil
        }
        if meta == nil {
            return runtime.Executed({{ .Names.FailedToolResult }}(call.Name, errors.New("tool call meta is nil"))), nil
        }
        switch call.Name {
        {{- range .Tools }}
        {{- if .IsMethodBacked }}
            {{- $toolHasSource := false }}
            {{- range .ServerData }}
                {{- if .MethodResultField }}
                    {{- $toolHasSource = true }}
                {{- end }}
            {{- end }}
            {{- $hasBoundsProjection := and .Bounds .Bounds.Projection .Bounds.Projection.Returned .Bounds.Projection.Truncated }}
        case tools.Ident({{ printf "%q" .QualifiedName }}):
            var toolArgs any
            {{- if .MethodPayloadTypeRef }}
            {
                val, err := {{ $.Toolset.SpecsPackageName }}.{{ .PayloadCodecName }}().FromJSON(call.Payload)
                if err != nil {
                    return runtime.Executed({{ $.Names.InvalidToolCall }}(
                        call,
                        err,
                    )), nil
                }
                {{- if .Injected }}
                if err := {{ $.Toolset.SpecsPackageName }}.{{ .InjectFunc }}(val, *meta, call.Labels); err != nil {
                    return runtime.Executed({{ $.Names.FailedCallResult }}(
                        call,
                        err,
                    )), nil
                }
                {{- end }}
                toolArgs = val
            }
            {{- end }}
            var methodIn any
            if cfg.mapPayload != nil {
                var err error
                methodIn, err = cfg.mapPayload(call.Name, toolArgs, meta)
                if err != nil {
                    return runtime.Executed({{ $.Names.FailedCallResult }}(
                        call,
                        err,
                    )), nil
                }
            } else {
                {{- if .MethodPayloadTypeRef }}
                    {{- if .PayloadAliasesMethod }}
                methodIn = toolArgs
                    {{- else }}
                methodIn = {{ $.Toolset.SpecsPackageName }}.{{ .MethodPayloadTransform }}(toolArgs.({{ if .PayloadPointer }}*{{ end }}{{ $.Toolset.SpecsPackageName }}.{{ .PayloadTypeName }}))
                    {{- end }}
                {{- end }}
            }
            for _, inj := range cfg.injectors {
                if err := inj.Inject(ctx, methodIn, meta); err != nil {
                    return runtime.Executed({{ $.Names.FailedCallResult }}(
                        call,
                        err,
                    )), nil
                }
            }
            methodOut, err := cfg.{{ .CallerField }}(ctx, methodIn)
            if err != nil {
                return runtime.Executed({{ $.Names.FailedCallResult }}(
                    call,
                    err,
                )), nil
            }
            var result any
            if cfg.mapResult != nil {
                var e error
                result, e = cfg.mapResult(call.Name, methodOut, meta)
                if e != nil {
                    return runtime.Executed({{ $.Names.FailedCallResult }}(
                        call,
                        e,
                    )), nil
                }
            } else {
                {{- if .HasResult }}
                    {{- if .ResultAliasesMethod }}
                result = methodOut
                    {{- else }}
                result = {{ $.Toolset.SpecsPackageName }}.{{ .ToolResultTransform }}(methodOut.({{ .MethodResultTypeRef }}))
                    {{- end }}
                {{- end }}
            }
            {{- if or $hasBoundsProjection $toolHasSource }}
            mr, ok := methodOut.({{ .MethodResultTypeRef }})
            if !ok {
                return runtime.Executed({{ $.Names.FailedCallResult }}(
                    call,
                    fmt.Errorf("unexpected method result type for %q: %T", call.Name, methodOut),
                )), nil
            }
            {{- end }}
            {{- if $hasBoundsProjection }}
            bounds := {{ .BoundsFunc }}(mr)
            {{- end }}
            {{- if $toolHasSource }}
            var serverItems []*toolregistry.ServerDataItem
            {{- range .ServerData }}
            {{- if .MethodResultField }}
            {
                data := {{ $.Toolset.SpecsPackageName }}.{{ .Transform }}(mr.{{ .MethodResultFieldName }})
                dataJSON, err := {{ $.Toolset.SpecsPackageName }}.{{ .CodecName }}().ToJSON(data)
                if err != nil {
                    return runtime.Executed({{ $.Names.FailedCallResult }}(
                        call,
                        err,
                    )), nil
                }
                if string(dataJSON) != "null" {
                    serverItems = append(serverItems, &toolregistry.ServerDataItem{
                        Kind:     {{ printf "%q" .Kind }},
                        Audience: {{ printf "%q" .Audience }},
                        Data:     dataJSON,
                    })
                }
            }
            {{- end }}
            {{- end }}
            var serverData rawjson.Message
            if len(serverItems) > 0 {
                b, err := json.Marshal(serverItems)
                if err != nil {
                    return runtime.Executed({{ $.Names.FailedCallResult }}(
                        call,
                        err,
                    )), nil
                }
                serverData = rawjson.Message(b)
            }
            {{- end }}
            return runtime.Executed(&planner.ToolResult{
                Name:   call.Name,
                Result: result,
                {{- if $hasBoundsProjection }}
                Bounds: bounds,
                {{- end }}
                {{- if $toolHasSource }}
                ServerData: serverData,
                {{- end }}
            }), nil
        {{- end }}
        {{- end }}
        default:
            return runtime.Executed({{ $.Names.FailedToolResult }}(
                call.Name,
                fmt.Errorf("unknown tool %q for toolset %q", call.Name, "{{ .Toolset.QualifiedName }}"),
            )), nil
        }
    })
}

// {{ .Names.FailedToolResult }} returns an internal failure that ends this run.
func {{ .Names.FailedToolResult }}(name tools.Ident, err error) *planner.ToolResult {
    return &planner.ToolResult{
        Name: name,
        Failure: &planner.ToolFailure{
            Kind:  planner.FailureInternal,
            Error: planner.ToolErrorFromError(err),
            Recovery: planner.RecoveryDirective{
                Action: planner.RecoveryFinish,
            },
        },
    }
}

// {{ .Names.FailedCallResult }} preserves a failure supplied by the service.
// Other errors become internal failures that end the run.
func {{ .Names.FailedCallResult }}(call *runtime.ToolCall, err error) *planner.ToolResult {
    var provider planner.ToolFailureProvider
    if !errors.As(err, &provider) {
        return {{ .Names.FailedToolResult }}(call.Name, err)
    }
    failure := planner.CloneToolFailure(provider.ToolFailure(call.Name))
    return &planner.ToolResult{
        Name:    call.Name,
        Failure: failure,
    }
}

// {{ .Names.InvalidToolCall }} returns the validation issues needed to correct a tool request.
func {{ .Names.InvalidToolCall }}(call *runtime.ToolCall, err error) *planner.ToolResult {
    var issuer interface {
        Issues() []*tools.FieldIssue
    }
    var issues []*tools.FieldIssue
    if errors.As(err, &issuer) {
        issues = issuer.Issues()
    }
    return &planner.ToolResult{
        Name: call.Name,
        Failure: &planner.ToolFailure{
            Kind:  planner.FailureInvalidCall,
            Error: planner.ToolErrorFromError(err),
            Recovery: planner.RecoveryDirective{
                Action: planner.RecoveryCorrectCall,
                Issues: issues,
            },
        },
    }
}

{{- range .Tools }}
{{- if and .IsMethodBacked .Bounds .Bounds.Projection .Bounds.Projection.Returned .Bounds.Projection.Truncated }}

// {{ .BoundsFunc }} copies result size and continuation values from the service result.
func {{ .BoundsFunc }}(mr {{ .MethodResultTypeRef }}) *agent.Bounds {
    bounds := &agent.Bounds{}
    {{- with .Bounds.Projection.Returned }}
    bounds.Returned = mr.{{ .Name }}
    {{- end }}
    {{- with .Bounds.Projection.Total }}
        {{- if .Required }}
    total := mr.{{ .Name }}
    bounds.Total = &total
        {{- else }}
    bounds.Total = mr.{{ .Name }}
        {{- end }}
    {{- end }}
    {{- with .Bounds.Projection.Truncated }}
    bounds.Truncated = mr.{{ .Name }}
    {{- end }}
    {{- with .Bounds.Projection.NextCursor }}
    bounds.NextCursor = mr.{{ .Name }}
    {{- end }}
    {{- with .Bounds.Projection.RefinementHint }}
        {{- if .Required }}
    bounds.RefinementHint = mr.{{ .Name }}
        {{- else }}
    if mr.{{ .Name }} != nil {
        bounds.RefinementHint = *mr.{{ .Name }}
    }
        {{- end }}
    {{- end }}
    return bounds
}
{{- end }}
{{- end }}
