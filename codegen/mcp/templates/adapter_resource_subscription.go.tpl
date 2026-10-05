{{- with .ResourceSubscription }}
// SubscriptionsListen supplies requested URIs to the configured resource source.
// Goa endpoint authentication and middleware run before that source can send
// an acknowledgment. The shared transport owns the final result's request ID.
func (a *MCPAdapter) SubscriptionsListen(ctx context.Context, p {{ index $.PayloadRefs "subscriptions/listen" }}) (*SubscriptionsListenResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.subscriptions/listen")
    defer span.End()
    body := &{{ .PayloadTransportRef }}{}
    body.{{ .Resources.Selector }} = make({{ .Resources.ValueTypeRef }}, len(p.Notifications.ResourceSubscriptions))
    for index, uri := range p.Notifications.ResourceSubscriptions {
        body.{{ .Resources.Selector }}[index] = {{ .Resources.ElementTypeRef }}(uri)
    }
    payload, err := {{ $.CodecPackage }}.{{ .PayloadConstructor }}(body)
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return nil, goa.PermanentError("invalid_params", "%s", err.Error())
    }
    {{- if or .Endpoint.Credentials .Endpoint.Paths }}
    if err := fill{{ .Endpoint.CallName }}Inputs(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "subscriptions/listen" }}{{ end }}{{ range .Endpoint.Paths }}, p.{{ index .Sources "subscriptions/listen" }}{{ end }}); err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return nil, err
    }
    {{- end }}
    ctx = context.WithValue(ctx, goa.ServiceKey, {{ quote $.ServiceName }})
    ctx = context.WithValue(ctx, goa.MethodKey, {{ quote .Endpoint.DesignMethodName }})
    stream := &resourceSubscriptionStream{ctx: ctx}
    defer stream.close()
    _, err = a.endpoints.{{ .Endpoint.MethodName }}(ctx, &{{ .InputRef }}{Payload: payload, Stream: stream})
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return nil, a.mapError(err, {{ if .Endpoint.FaultNames }}is{{ .Endpoint.CallName }}Fault{{ else }}isEndpointFault{{ end }}(err)).err
    }
    return &SubscriptionsListenResult{ResultType: "complete", Meta: resultMeta()}, nil
}

// resourceSubscriptionStream validates authored values before sending them on
// this request. Closing it prevents retained service references from sending
// further values; normal method return supplies the finished protocol result.
type resourceSubscriptionStream struct {
    ctx context.Context
    mu sync.Mutex
    closed bool
}

// {{ .SendName }} sends a typed acknowledgment or update on the source request.
func (s *resourceSubscriptionStream) {{ .SendName }}(event {{ .Endpoint.ResultRef }}) error {
    return s.{{ .SendWithContextName }}(s.ctx, event)
}

// {{ .SendWithContextName }} keeps request correlation while honoring this send's
// cancellation. Invalid service values return a server error before any bytes.
func (s *resourceSubscriptionStream) {{ .SendWithContextName }}(ctx context.Context, event {{ .Endpoint.ResultRef }}) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.closed {
        return goa.PermanentError("internal_error", "resource subscription source is closed")
    }
    if err := ctx.Err(); err != nil {
        return err
    }
    if err := {{ .Codec.ResultValidate }}(event); err != nil {
        return goa.PermanentError("internal_error", "%s", err.Error())
    }
    sendContext, cancel := context.WithCancel(s.ctx)
    stop := context.AfterFunc(ctx, cancel)
    defer stop()
    defer cancel()
    choice := {{ .UnionRef }}(event.{{ .ChangeField }})
    switch choice.Kind() {
    case {{ .AcknowledgedKind }}:
        selected, _ := choice.AsAcknowledged()
        uris := make([]string, len(selected.{{ .AcknowledgedResources }}))
        for index, uri := range selected.{{ .AcknowledgedResources }} {
            uris[index] = string(uri)
        }
        return mcpruntime.AcknowledgeSubscription(sendContext, mcpruntime.SubscriptionFilter{ResourceSubscriptions: uris})
    case {{ .UpdatedKind }}:
        selected, _ := choice.AsUpdated()
        return mcpruntime.ReportResourceUpdated(sendContext, string({{ .UpdatedURIValue }}))
    default:
        return goa.PermanentError("internal_error", "resource subscription source returned an undeclared event")
    }
}

{{- if .MustClose }}
// Close stops authored sends. The method's return completes the MCP response.
func (s *resourceSubscriptionStream) Close() error {
    s.close()
    return nil
}
{{- end }}

// close prevents sends through a source reference after its method finishes.
func (s *resourceSubscriptionStream) close() {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.closed = true
}
{{- end }}
