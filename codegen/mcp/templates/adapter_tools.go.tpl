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
                {{- if .HasResult }}
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
            {Type: "text", Text: message},
        },
        IsError: boolPtr(true),
    }
}

// ToolsCall decodes the named tool's arguments through its generated codec,
// calls its service, and encodes one structured result through that same contract.
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
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.service.{{ .ServiceMethodName }}(ctx, payload)
        {{- else }}
        result, err := a.service.{{ .ServiceMethodName }}(ctx)
        {{- end }}
        if err != nil {
            failure := a.mapError(err)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return toolCallError(failure.Error()), nil
        }
        encoded, err := {{ $.CodecPackage }}.{{ .Codec.ResultEncode }}(result)
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        return &ToolsCallResult{
            ResultType: "complete",
            Meta: resultMeta(),
            Content: []*ContentItem{},
            StructuredContent: json.RawMessage(encoded),
        }, nil
        {{- else }}
        {{- if .HasPayload }}
        err := a.service.{{ .ServiceMethodName }}(ctx, payload)
        {{- else }}
        err := a.service.{{ .ServiceMethodName }}(ctx)
        {{- end }}
        if err != nil {
            failure := a.mapError(err)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return toolCallError(failure.Error()), nil
        }
        return &ToolsCallResult{ResultType: "complete", Meta: resultMeta(), Content: []*ContentItem{}}, nil
        {{- end }}
    {{- end }}
    default:
        return nil, goa.PermanentError("invalid_params", "unknown tool: %s", p.Name)
    }
}
{{- end }}
