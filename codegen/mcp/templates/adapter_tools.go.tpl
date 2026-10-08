{{- if .Tools }}
// ToolsList returns either the fixed catalog or an authenticated page of
// declared tool names. The generated tool definitions keep their exact schemas.
func (a *MCPAdapter) ToolsList(ctx context.Context, p {{ index .PayloadRefs "tools/list" }}) (*ToolsListResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.tools/list")
    defer span.End()
    {{- with .ToolCatalog }}
    {{ template "catalog-page" . }}
    tools := make([]*ToolInfo, 0, len({{ .Endpoint.ResultValue }}.{{ .EntriesField }}))
    seen := make(map[string]struct{}, len({{ .Endpoint.ResultValue }}.{{ .EntriesField }}))
    for _, name := range {{ .Endpoint.ResultValue }}.{{ .EntriesField }} {
        selected := string({{ if .NamePointer }}*{{ end }}name)
        if _, duplicate := seen[selected]; duplicate {
            failure := goa.PermanentError("internal_error", "tool catalog returned duplicate name %q", selected)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return nil, failure
        }
        seen[selected] = struct{}{}
        switch selected {
        {{- range $.Tools }}
        case {{ quote .Name }}:
            tools = append(tools, &ToolInfo{{ template "tool-info" . }})
        {{- end }}
        default:
            failure := goa.PermanentError("internal_error", "tool catalog returned undeclared name %q", selected)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return nil, failure
        }
    }
    {{- else }}
    if p.Cursor != nil {
        failure := goa.PermanentError("invalid_params", "tools/list does not accept a cursor")
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    tools := []*ToolInfo{
        {{- range .Tools }}
        {{ template "tool-info" . }},
        {{- end }}
    }
    {{- end }}
    return &ToolsListResult{
        ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Tools: tools,
        {{- if .ToolCatalog }}NextCursor: nextCursor,{{ end }}
    }, nil
}

// toolCallError lets the client correct a recognized tool's arguments or
// observe an application failure without treating it as a protocol failure.
func toolCallError(message string) *ToolsCallResult {
    return &ToolsCallResult{Outcome: NewToolsCallOutcomeComplete(&ToolsCallCompleteResult{
        Meta: resultMeta(),
        Content: []*ContentItem{
            {Type: "text", Text: stringPtr(message)},
        },
        IsError: boolPtr(true),
    })}
}

// ToolsCall decodes the named tool's arguments through its generated codec,
// calls its configured endpoint, and encodes one structured result through that contract.
func (a *MCPAdapter) ToolsCall(ctx context.Context, p {{ index .PayloadRefs "tools/call" }}) (*ToolsCallResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.tools/call")
    defer span.End()
    switch p.Name {
    {{- range .Tools }}
    case {{ quote .Name }}:
        {{- if .Task }}
        if err := validateTaskCapabilities(p.Meta); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, err
        }
        {{- end }}
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
        {{- if or .Endpoint.Credentials .Endpoint.Paths .Endpoint.InputExchange }}
        if err := fill{{ .Endpoint.CallName }}Inputs(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "tools/call" }}{{ end }}{{ range .Endpoint.Paths }}, p.{{ index .Sources "tools/call" }}{{ end }}{{ if .Endpoint.InputExchange }}, p.RequestState, p.InputResponses{{ end }}); err != nil {
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
        {{- $endpoint := .Endpoint }}
        {{- with .Endpoint.InputExchange }}
        if err := {{ .OutcomeValue }}.Validate(); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        if pending, ok := {{ .OutcomeValue }}.AsInputRequired(); ok {
            input, err := convert{{ $endpoint.CallName }}Pending(pending, p.Meta)
            if err != nil {
                span.RecordError(err)
                span.SetStatus(codes.Error, err.Error())
                return nil, err
            }
            return &ToolsCallResult{Outcome: NewToolsCallOutcomeInputRequired(input)}, nil
        }
        {{- if or (not $endpoint.ExecutionView) $endpoint.TaskCreator }}
        completedResult, _ := {{ .OutcomeValue }}.AsComplete()
        {{- end }}
        {{- end }}
        {{- if .Task }}
        observation := {{ if .Endpoint.InputExchange }}completedResult{{ else }}{{ .Endpoint.ResultValue }}{{ end }}
        if err := {{ .Task.Created.Validate }}(observation); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        state := {{ .Task.Created.Name }}Metadata(observation.{{ .Task.Created.MetadataField }}, string(observation.{{ .Task.Created.OutcomeField }}.Kind()))
        return &ToolsCallResult{Outcome: NewToolsCallOutcomeTask(state)}, nil
        {{- else }}
        {{- if .Content }}
        content, encoded, err := {{ .Content.Name }}({{ if and .Endpoint.InputExchange (not .Endpoint.ExecutionView) }}completedResult{{ else }}result{{ end }})
        {{- else }}
        encoded, err := {{ .Codec.ResultEncode }}({{ if and .Endpoint.InputExchange (not .Endpoint.ExecutionView) }}completedResult{{ else }}result{{ end }})
        {{- end }}
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        return &ToolsCallResult{Outcome: NewToolsCallOutcomeComplete(&ToolsCallCompleteResult{
            Meta: resultMeta(),
            Content: {{ if .Content }}content{{ else }}[]*ContentItem{}{{ end }},
            StructuredContent: json.RawMessage(encoded),
        })}, nil
        {{- end }}
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
        return &ToolsCallResult{Outcome: NewToolsCallOutcomeComplete(&ToolsCallCompleteResult{Meta: resultMeta(), Content: []*ContentItem{}})}, nil
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
    {{- if .Validate }}
    if err := {{ .Validate }}(result); err != nil { return nil, nil, err }
    {{- end }}
    {{- if .OutcomeValue }}
    completedResult, _ := {{ .OutcomeValue }}.AsComplete()
    {{- end }}
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

{{- define "tool-info" -}}
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
            }
{{- end }}
