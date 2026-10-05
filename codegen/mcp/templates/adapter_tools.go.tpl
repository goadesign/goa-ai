{{- if .Tools }}
// ToolsList returns the stable catalog declared by the design. A supplied
// cursor is invalid because this generated catalog has only one page.
func (a *MCPAdapter) ToolsList(ctx context.Context, p *ToolsListPayload) (*ToolsListResult, error) {
    _, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.tools/list")
    defer span.End()
    if p.Cursor != nil {
        failure := goa.PermanentError("invalid_params", "tools/list does not accept a cursor")
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    return &ToolsListResult{
        ResultType: "complete",
        Meta: resultMeta(),
        TTLMs: 0,
        CacheScope: "private",
        Tools: []*ToolInfo{
        {{- range .Tools }}
            {
                Name: {{ quote .Name }},
                Description: stringPtr({{ quote .Description }}),
                {{- with .Annotations }}
                Annotations: &ToolAnnotations{
                    {{- if ne .Title nil }}Title: stringPtr({{ quote .Title }}),{{ end }}
                    {{- if ne .ReadOnlyHint nil }}ReadOnlyHint: boolPtr({{ .ReadOnlyHint }}),{{ end }}
                    {{- if ne .DestructiveHint nil }}DestructiveHint: boolPtr({{ .DestructiveHint }}),{{ end }}
                    {{- if ne .IdempotentHint nil }}IdempotentHint: boolPtr({{ .IdempotentHint }}),{{ end }}
                    {{- if ne .OpenWorldHint nil }}OpenWorldHint: boolPtr({{ .OpenWorldHint }}),{{ end }}
                },
                {{- end }}
                InputSchema: json.RawMessage({{ quote .InputSchema }}),
                {{- if .OutputSchema }}
                OutputSchema: json.RawMessage({{ quote .OutputSchema }}),
                {{- end }}
            },
        {{- end }}
        },
    }, nil
}

// toolCallError lets the client correct a recognized tool's arguments or
// observe an application failure without treating it as a protocol failure.
func toolCallError(message string) *ToolsCallResult {
    return &ToolsCallResult{
        ResultType: "complete",
        Meta: resultMeta(),
        Content: []*ContentItem{
            {Type: "text", Text: stringPtr(message)},
        },
        IsError: boolPtr(true),
    }
}

// ToolsCall decodes the named tool's arguments through its generated codec,
// calls its configured endpoint, and encodes one structured result through that contract.
func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload) (*ToolsCallResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.tools/call")
    defer span.End()
    switch p.Name {
    {{- range .Tools }}
    case {{ quote .Name }}:
        {{- if .HasPayload }}
        arguments := p.Arguments
        if len(arguments) == 0 {
            arguments = json.RawMessage("{}")
        }
        payload, err := {{ $.CodecPackage }}.{{ .Codec.PayloadDecode }}(arguments)
        if err != nil {
            return toolCallError("invalid arguments: " + err.Error()), nil
        }
        {{- else }}
        if err := validateNoArguments(p.Arguments); err != nil {
            return toolCallError("invalid arguments: " + err.Error()), nil
        }
        {{- end }}
        {{- if .Endpoint.Credentials }}
        if err := fill{{ .Endpoint.CallName }}Credentials(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "tools/call" }}{{ end }}); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return toolCallError(err.Error()), nil
        }
        {{- end }}
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.{{ .Endpoint.CallName }}(ctx, payload)
        {{- else }}
        result, err := a.{{ .Endpoint.CallName }}(ctx)
        {{- end }}
        if err != nil {
            failure := a.mapError(err, {{ if .Endpoint.FaultNames }}is{{ .Endpoint.CallName }}Fault{{ else }}isEndpointFault{{ end }}(err))
            span.RecordError(failure.err)
            span.SetStatus(codes.Error, failure.err.Error())
            if failure.internal {
                return nil, failure.err
            }
            return toolCallError(failure.err.Error()), nil
        }
        {{- if .Content }}
        content, encoded, err := {{ .Content.Name }}(result)
        {{- else }}
        encoded, err := {{ .Codec.ResultEncode }}(result)
        {{- end }}
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        return &ToolsCallResult{
            ResultType: "complete",
            Meta: resultMeta(),
            Content: {{ if .Content }}content{{ else }}[]*ContentItem{}{{ end }},
            StructuredContent: json.RawMessage(encoded),
        }, nil
        {{- else }}
        {{- if .HasPayload }}
        err = a.{{ .Endpoint.CallName }}(ctx, payload)
        {{- else }}
        err := a.{{ .Endpoint.CallName }}(ctx)
        {{- end }}
        if err != nil {
            failure := a.mapError(err, {{ if .Endpoint.FaultNames }}is{{ .Endpoint.CallName }}Fault{{ else }}isEndpointFault{{ end }}(err))
            span.RecordError(failure.err)
            span.SetStatus(codes.Error, failure.err.Error())
            if failure.internal {
                return nil, failure.err
            }
            return toolCallError(failure.err.Error()), nil
        }
        return &ToolsCallResult{ResultType: "complete", Meta: resultMeta(), Content: []*ContentItem{}}, nil
        {{- end }}
    {{- end }}
    default:
        return nil, goa.PermanentError("invalid_params", "unknown tool: %s", p.Name)
    }
}
{{- end }}

{{- range .Tools }}
{{- with .Content }}
// {{ .Name }} validates the returned service value and separates content from
// structured JSON. A service-selected view keeps both outputs within that view.
func {{ .Name }}(result {{ .SourceRef }}) ([]*ContentItem, json.RawMessage, error) {
    if err := {{ .Validate }}(result); err != nil { return nil, nil, err }
    {{- if .ExecutionView }}
    switch result.View {
    {{- end }}
    {{- $content := . }}
    {{- range .Cases }}
    {{- if $content.ExecutionView }}
    case {{ quote .Name }}:
    {{- end }}
        content := []*ContentItem{}
        {{- if .Field }}
        for _, value := range {{ .Value }}.{{ .Field }} {
            item, err := {{ .Convert }}(value.{{ .ElementField }})
            if err != nil { return nil, nil, err }
            content = append(content, item)
        }
        {{- end }}
        {{- if .Encode }}
        encoded, err := {{ .Encode }}({{ .Value }})
        if err != nil { return nil, nil, err }
        {{- if $content.ExecutionView }}
        encoded, err = json.Marshal(struct {
            Type string `json:"type"`
            Value json.RawMessage `json:"value"`
        }{Type: {{ quote .Name }}, Value: encoded})
        if err != nil { return nil, nil, err }
        {{- end }}
        return content, encoded, nil
        {{- else }}
        return content, nil, nil
        {{- end }}
    {{- end }}
    {{- if .ExecutionView }}
    default:
        return nil, nil, fmt.Errorf("endpoint returned undeclared result view %q", result.View)
    }
    {{- end }}
}
{{- end }}
{{- end }}
