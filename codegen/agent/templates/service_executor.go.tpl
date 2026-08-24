// Default service executor for {{ .Toolset.Name }}
// This factory builds a runtime.ToolCallExecutor that dispatches tool calls to
// user-provided per-tool callers. It decodes tool payloads with generated codecs,
// allows optional payload/result mappers, and returns results as-is (or mapped).
//
// The executor automatically wires the provided service client to the tool callers.
// You can override individual callers using the generated With<Tool> options.
//
// Example:
//
//   client := atlasdata.NewClient(...)
//   exec := {{ .Toolset.PackageName }}.New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec(client)
//
//   // Register:
//   // reg := {{ .Agent.GoName }}{{ goify .Toolset.PathName true }}.New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}ToolsetRegistration(exec)
//   // rt.RegisterToolset(reg)

type (
    seCfg struct {
        callers    map[tools.Ident]func(context.Context, any) (any, error)
        mapPayload func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)
        mapResult  func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)
        injectors  []ToolInterceptor
    }
    // ExecOpt customizes the default service executor.
    ExecOpt interface{ apply(*seCfg) }

    // ToolInterceptor hooks into tool execution to inject context or modify payloads.
    ToolInterceptor interface {
        // Inject mutates the service method payload before the client call.
        // It receives the fully mapped service payload (e.g. *GetAlarmsPayload)
        // and the tool call metadata.
        Inject(ctx context.Context, payload any, meta *runtime.ToolCallMeta) error
    }
    
    ToolInterceptorFunc func(context.Context, any, *runtime.ToolCallMeta) error
)

func (f ToolInterceptorFunc) Inject(ctx context.Context, p any, m *runtime.ToolCallMeta) error {
    return f(ctx, p, m)
}

type execOptFunc func(*seCfg)

func (f execOptFunc) apply(c *seCfg) { f(c) }

// WithPayloadMapper installs a mapper for tool payload -> method payload.
func WithPayloadMapper(f func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) { c.mapPayload = f })
}

// WithResultMapper installs a mapper for method result -> tool result.
func WithResultMapper(f func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) { c.mapResult = f })
}

// WithInterceptors adds interceptors to the executor.
func WithInterceptors(interceptors ...ToolInterceptor) ExecOpt {
    return execOptFunc(func(c *seCfg) {
        c.injectors = append(c.injectors, interceptors...)
    })
}

// WithClient wires default callers for all method-backed tools using the
// provided service client. This is a convenience for direct service wiring;
// adapter-style executors can instead provide callers via the With<Tool>
// options without supplying a client.
func WithClient(client *{{ .ServicePkgAlias }}.Client) ExecOpt {
    return execOptFunc(func(c *seCfg) {
        if client == nil {
            return
        }
        if c.callers == nil {
            c.callers = make(map[tools.Ident]func(context.Context, any) (any, error))
        }
		{{- range .Tools }}
		{{- if .Tool.IsMethodBacked }}
		c.callers[tools.Ident({{ printf "%q" .Tool.QualifiedName }})] = func(ctx context.Context, args any) (any, error) {
			{{- if .Tool.HasResult }}
				{{- if .Tool.MethodPayloadTypeRef }}
			return client.{{ .Tool.MethodGoName }}(ctx, args.({{ .Tool.MethodPayloadTypeRef }}))
				{{- else }}
			return client.{{ .Tool.MethodGoName }}(ctx)
				{{- end }}
			{{- else }}
				{{- if .Tool.MethodPayloadTypeRef }}
			err := client.{{ .Tool.MethodGoName }}(ctx, args.({{ .Tool.MethodPayloadTypeRef }}))
				{{- else }}
			err := client.{{ .Tool.MethodGoName }}(ctx)
                {{- end }}
            return nil, err
            {{- end }}
        }
        {{- end }}
        {{- end }}
    })
}

{{- range .Tools }}
{{- if .Tool.IsMethodBacked }}
// With{{ goify .Tool.Name true }} sets the caller for {{ .Tool.QualifiedName }}.
func With{{ goify .Tool.Name true }}(f func(context.Context, any) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) {
        if c.callers == nil {
            c.callers = make(map[tools.Ident]func(context.Context, any) (any, error))
        }
		c.callers[tools.Ident({{ printf "%q" .Tool.QualifiedName }})] = f
    })
}
{{- end }}
{{- end }}

// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec returns a ToolCallExecutor that
// decodes tool payloads with generated codecs, applies optional mappers, calls user-provided
// per-tool callers (wired from the client via WithClient), and maps results back.
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec(opts ...ExecOpt) runtime.ToolCallExecutor {
    var cfg seCfg
    cfg.callers = make(map[tools.Ident]func(context.Context, any) (any, error))

    for _, o := range opts {
        if o != nil {
            o.apply(&cfg)
        }
    }
    // Preflight: ensure callers are provided for all method-backed tools.
    {
        var missing []string
		{{- range .Tools }}
		{{- if .Tool.IsMethodBacked }}
		if cfg.callers == nil || cfg.callers[tools.Ident({{ printf "%q" .Tool.QualifiedName }})] == nil {
			// report the fully-qualified tool for clarity
			missing = append(missing, {{ printf "%q" .Tool.QualifiedName }})
        }
        {{- end }}
        {{- end }}
        if len(missing) > 0 {
            panic(fmt.Errorf("service executor missing callers for tools: %s", strings.Join(missing, ", ")))
        }
    }
    return runtime.ToolCallExecutorFunc(func(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*runtime.ToolExecutionResult, error) {
        if call == nil {
            return runtime.Executed(failedServiceToolResult("", errors.New("tool request is nil"))), nil
        }
        if meta == nil {
            return runtime.Executed(failedServiceToolResult(call.Name, errors.New("tool call meta is nil"))), nil
        }
        switch call.Name {
		{{- range .Tools }}
		{{- if .Tool.IsMethodBacked }}
			{{- $toolHasSource := false }}
			{{- range .ServerData }}
				{{- if .Data.MethodResultField }}
					{{- $toolHasSource = true }}
				{{- end }}
			{{- end }}
			{{- $hasBoundsProjection := and .Tool.Bounds .Tool.Bounds.Projection .Tool.Bounds.Projection.Returned .Tool.Bounds.Projection.Truncated }}
		case tools.Ident({{ printf "%q" .Tool.QualifiedName }}):
            caller := cfg.callers[call.Name]
            if caller == nil {
                panic(fmt.Errorf("service executor missing caller for tool %q", call.Name))
            }
            var toolArgs any
			{{- if .Tool.MethodPayloadTypeRef }}
			{
				val, err := {{ $.Toolset.SpecsPackageName }}.{{ .Spec.Payload.ExportedCodec }}.FromJSON(call.Payload)
                if err != nil {
                    return runtime.Executed(invalidServiceToolCall(
                        call,
                        err,
						{{ $.Toolset.SpecsPackageName }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON,
                    )), nil
                }
				{{- if .Tool.Injected }}
				if err := {{ $.Toolset.SpecsPackageName }}.{{ .Spec.InjectFunc }}(val, *meta, call.Labels); err != nil {
                    return runtime.Executed(failedServiceCallResult(
                        call,
                        err,
						{{ $.Toolset.SpecsPackageName }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON,
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
                    return runtime.Executed(failedServiceCallResult(
                        call,
                        err,
						{{ $.Toolset.SpecsPackageName }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON,
                    )), nil
                }
            } else {
				{{- if .Tool.MethodPayloadTypeRef }}
					{{- if .Tool.PayloadAliasesMethod }}
				methodIn = toolArgs
					{{- else }}
				methodIn = {{ $.Toolset.SpecsPackageName }}.{{ .Spec.MethodPayloadTransform }}(toolArgs.(*{{ $.Toolset.SpecsPackageName }}.{{ .Spec.Payload.TypeName }}))
                    {{- end }}
                {{- end }}
            }
            for _, inj := range cfg.injectors {
                if err := inj.Inject(ctx, methodIn, meta); err != nil {
                    return runtime.Executed(failedServiceCallResult(
                        call,
                        err,
						{{ $.Toolset.SpecsPackageName }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON,
                    )), nil
                }
            }
            methodOut, err := caller(ctx, methodIn)
            if err != nil {
                return runtime.Executed(failedServiceCallResult(
                    call,
                    err,
					{{ $.Toolset.SpecsPackageName }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON,
                )), nil
            }
            var result any
            if cfg.mapResult != nil {
                var e error
                result, e = cfg.mapResult(call.Name, methodOut, meta)
                if e != nil {
                    return runtime.Executed(failedServiceCallResult(
                        call,
                        e,
						{{ $.Toolset.SpecsPackageName }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON,
                    )), nil
                }
            } else {
				{{- if .Tool.HasResult }}
					{{- if .Tool.ResultAliasesMethod }}
				result = methodOut
					{{- else }}
				result = {{ $.Toolset.SpecsPackageName }}.{{ .Spec.ToolResultTransform }}(methodOut.({{ .Tool.MethodResultTypeRef }}))
                    {{- end }}
                {{- end }}
            }
            {{- if or $hasBoundsProjection $toolHasSource }}
			mr, ok := methodOut.({{ .Tool.MethodResultTypeRef }})
            if !ok {
                return runtime.Executed(failedServiceCallResult(
                    call,
                    fmt.Errorf("unexpected method result type for %q: %T", call.Name, methodOut),
					{{ $.Toolset.SpecsPackageName }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON,
                )), nil
            }
            {{- end }}
            {{- if $hasBoundsProjection }}
			bounds := init{{ goify .Tool.Name true }}Bounds(mr)
            {{- end }}
            {{- if $toolHasSource }}
            var serverItems []*toolregistry.ServerDataItem
            {{- $tool := . }}
            {{- range .ServerData }}
			{{- if .Data.MethodResultField }}
			{
				data := {{ $.Toolset.SpecsPackageName }}.{{ .Spec.Transform }}(mr.{{ goify .Data.MethodResultField true }})
				dataJSON, err := {{ $.Toolset.SpecsPackageName }}.{{ .Spec.Type.ExportedCodec }}.ToJSON(data)
                if err != nil {
                    return runtime.Executed(failedServiceCallResult(
                        call,
                        err,
						{{ $.Toolset.SpecsPackageName }}.{{ $tool.Spec.SpecVar }}.Payload.ExampleJSON,
                    )), nil
                }
                if string(dataJSON) != "null" {
                    serverItems = append(serverItems, &toolregistry.ServerDataItem{
						Kind:     {{ printf "%q" .Data.Kind }},
						Audience: {{ printf "%q" .Data.Audience }},
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
                    return runtime.Executed(failedServiceCallResult(
                        call,
                        err,
						{{ $.Toolset.SpecsPackageName }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON,
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
            return runtime.Executed(failedServiceToolResult(
                call.Name,
                fmt.Errorf("unknown tool %q for toolset %q", call.Name, "{{ .Toolset.QualifiedName }}"),
            )), nil
        }
    })
}

// failedServiceToolResult converts a service-executor invariant failure into a
// terminal internal tool failure.
func failedServiceToolResult(name tools.Ident, err error) *planner.ToolResult {
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

// failedServiceCallResult applies a service-owned failure classification at
// every stage of an admitted call and attaches call-owned correction data.
func failedServiceCallResult(call *planner.ToolRequest, err error, example rawjson.Message) *planner.ToolResult {
    var provider planner.ToolFailureProvider
    if !errors.As(err, &provider) {
        return failedServiceToolResult(call.Name, err)
    }
    failure := planner.CloneToolFailure(provider.ToolFailure(call.Name))
    if failure.Recovery.Action == planner.RecoveryCorrectCall {
        failure.Recovery.PriorInput = append(rawjson.Message(nil), call.Payload...)
        failure.Recovery.ExampleJSON = append(rawjson.Message(nil), example...)
    }
    return &planner.ToolResult{
        Name:    call.Name,
        Failure: failure,
    }
}

// invalidServiceToolCall preserves generated validation issues and canonical
// payload metadata for a same-tool correction turn.
func invalidServiceToolCall(call *planner.ToolRequest, err error, example rawjson.Message) *planner.ToolResult {
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
                Action:      planner.RecoveryCorrectCall,
                Issues:      issues,
                PriorInput:  append(rawjson.Message(nil), call.Payload...),
                ExampleJSON: example,
            },
        },
    }
}

{{- range .Tools }}
{{- if and .Tool.IsMethodBacked .Tool.Bounds .Tool.Bounds.Projection .Tool.Bounds.Projection.Returned .Tool.Bounds.Projection.Truncated }}
{{- $tool := . }}

// init{{ goify .Tool.Name true }}Bounds copies the bounded result fields from
// the service method result.
func init{{ goify .Tool.Name true }}Bounds(mr {{ .Tool.MethodResultTypeRef }}) *agent.Bounds {
    bounds := &agent.Bounds{}
    {{- with .Tool.Bounds.Projection.Returned }}
    bounds.Returned = mr.{{ .Name }}
    {{- end }}
    {{- with .Tool.Bounds.Projection.Total }}
        {{- if .Required }}
    total := mr.{{ .Name }}
    bounds.Total = &total
        {{- else }}
    bounds.Total = mr.{{ .Name }}
        {{- end }}
    {{- end }}
    {{- with .Tool.Bounds.Projection.Truncated }}
    bounds.Truncated = mr.{{ .Name }}
    {{- end }}
    {{- with .Tool.Bounds.Projection.NextCursor }}
    bounds.NextCursor = mr.{{ .Name }}
    {{- end }}
    {{- with .Tool.Bounds.Projection.RefinementHint }}
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

