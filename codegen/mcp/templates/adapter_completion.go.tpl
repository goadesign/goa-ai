{{- if .Completions }}
// CompletionComplete selects a declared prompt or resource argument and asks its service
// method for suggestions. Missing bindings return an empty list for a valid
// argument; unknown names fail before any service method runs.
func (a *MCPAdapter) CompletionComplete(ctx context.Context, p {{ index .PayloadRefs "completion/complete" }}) (*CompletionCompleteResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.completion/complete")
    defer span.End()
    var reference string
    switch {
    case p.Ref.Type == "ref/prompt" && p.Ref.Name != nil:
        reference = *p.Ref.Name
    case p.Ref.Type == "ref/resource" && p.Ref.URI != nil:
        reference = *p.Ref.URI
    default:
        failure := goa.PermanentError("invalid_params", "unknown completion reference")
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    switch {
    {{ range .CompletionReferences }}
    case p.Ref.Type == {{ quote .Type }} && reference == {{ quote .Name }}:
        switch p.Argument.Name {
        {{ range .Arguments }}
        case {{ quote . }}:
        {{ end }}
        default:
            failure := goa.PermanentError("invalid_params", "unknown completion argument: %s", p.Argument.Name)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return nil, failure
        }
        if p.Context != nil {
            for name := range p.Context.Arguments {
                switch name {
                {{ range .Arguments }}
                case {{ quote . }}:
                {{ end }}
                default:
                    failure := goa.PermanentError("invalid_params", "unknown context argument: %s", name)
                    span.RecordError(failure)
                    span.SetStatus(codes.Error, failure.Error())
                    return nil, failure
                }
            }
        }
    {{ end }}
    default:
        failure := goa.PermanentError("invalid_params", "unknown completion reference: %s", reference)
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    {{ range .Completions }}
    if p.Ref.Type == {{ quote .ReferenceType }} && reference == {{ quote .Reference }} && p.Argument.Name == {{ quote .Argument }} {
        // The private constructor applies the method's defaults and validation
        // to already-decoded protocol values before the service receives them.
        body := &{{ .PayloadTransportRef }}{}
        value := {{ .Value.ValueTypeRef }}(p.Argument.Value)
        body.{{ .Value.Selector }} = &value
        if p.Context != nil && p.Context.Arguments != nil {
            body.{{ .Arguments.Selector }} = make({{ .Arguments.ValueTypeRef }}, len(p.Context.Arguments))
            for name, value := range p.Context.Arguments {
                body.{{ .Arguments.Selector }}[{{ .Arguments.KeyTypeRef }}(name)] = {{ .Arguments.ElementTypeRef }}(value)
            }
        }
        payload, err := {{ $.CodecPackage }}.{{ .PayloadConstructor }}(body)
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- if or .Endpoint.Credentials .Endpoint.Paths }}
        if err := fill{{ .Endpoint.CallName }}Inputs(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "completion/complete" }}{{ end }}{{ range .Endpoint.Paths }}, p.{{ index .Sources "completion/complete" }}{{ end }}); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, err
        }
        {{- end }}
        result, err := a.{{ .Endpoint.CallName }}(ctx, payload)
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, a.mapError(err, {{ if .Endpoint.FaultNames }}is{{ .Endpoint.CallName }}Fault{{ else }}isEndpointFault{{ end }}(err)).err
        }
        if err := {{ .Codec.ResultValidate }}(result); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        {{ .Conversion }}
        if len(out.Values) == 0 { out.Values = []string{} }
        return &CompletionCompleteResult{ResultType: "complete", Meta: resultMeta(), Completion: out}, nil
    }
    {{ end }}
    return &CompletionCompleteResult{ResultType: "complete", Meta: resultMeta(), Completion: &CompletionSuggestion{Values: []string{}}}, nil
}
{{ range .Completions }}
{{ range .Helpers }}
{{ .Source }}
{{ end }}
{{ end }}
{{- end }}
