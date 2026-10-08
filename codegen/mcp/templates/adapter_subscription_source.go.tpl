{{- with .SubscriptionSource }}
// SubscriptionsListen supplies native resource and job selections to the source.
// Goa endpoint authentication and middleware run before that source can send
// an acknowledgment. The shared transport owns the final result's request ID.
func (a *MCPAdapter) SubscriptionsListen(ctx context.Context, p {{ index $.PayloadRefs "subscriptions/listen" }}) (*SubscriptionsListenResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.subscriptions/listen")
    defer span.End()
    body := &{{ .PayloadTransportRef }}{}
    {{- if .Resources }}
    body.{{ .Resources.Selector }} = make({{ .Resources.ValueTypeRef }}, len(p.Notifications.ResourceSubscriptions))
    for index, uri := range p.Notifications.ResourceSubscriptions {
        body.{{ .Resources.Selector }}[index] = {{ .Resources.ElementTypeRef }}(uri)
    }
    {{- end }}
    {{- if .Tasks }}
    body.{{ .TaskInput.Selector }} = &{{ .TaskInput.ValueTypeRef }}{}
    {{- range .Tasks }}
    {{ .Prefix }}IDs := make(map[string][]string)
    {{- end }}
    for _, taskID := range p.Notifications.TaskIds {
        key, nativeID, err := decodeTaskID(taskID)
        if err != nil { return nil, err }
        switch key {
        {{- range .Tasks }}
        {{- $selection := . }}
        {{- range .Tools }}
        case {{ quote .Key }}:
            _, requested := {{ $selection.Prefix }}IDs[nativeID]
            {{ $selection.Prefix }}IDs[nativeID] = append({{ $selection.Prefix }}IDs[nativeID], taskID)
            if !requested {
                native := {{ $selection.Transport.ElementTypeRef }}(nativeID)
                body.{{ $.SubscriptionSource.TaskInput.Selector }}.{{ $selection.Transport.Selector }} = append(body.{{ $.SubscriptionSource.TaskInput.Selector }}.{{ $selection.Transport.Selector }}, {{ if $selection.Transport.ElementPointer }}&{{ end }}native)
            }
        {{- end }}
        {{- end }}
        }
    }
    {{- end }}
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
    stream := &subscriptionStream{ctx: ctx{{ if .Tasks }}, adapter:a, input:p{{ range .Tasks }}, {{ .Prefix }}Requested: {{ .Prefix }}IDs{{ end }}{{ end }}}
    defer stream.close()
    _, err = a.endpoints.{{ .Endpoint.MethodName }}(ctx, &{{ .InputRef }}{Payload: payload, Stream: stream})
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return nil, a.mapError(err, {{ if .Endpoint.FaultNames }}is{{ .Endpoint.CallName }}Fault{{ else }}isEndpointFault{{ end }}(err)).err
    }
    return &SubscriptionsListenResult{ResultType: "complete", Meta: resultMeta()}, nil
}

// subscriptionStream validates authored values before sending them on
// this request. Closing it prevents retained service references from sending
// further values; normal method return supplies the finished protocol result.
type subscriptionStream struct {
    ctx context.Context
    mu sync.Mutex
    closed bool
    {{- if .Tasks }}
    adapter *MCPAdapter
    input {{ index $.PayloadRefs "subscriptions/listen" }}
    {{- range .Tasks }}
    {{ .Prefix }}Requested map[string][]string
    {{ .Prefix }}Accepted map[string][]string
    {{- end }}
    {{- end }}
}

// {{ .SendName }} sends a typed acknowledgment or update on the source request.
func (s *subscriptionStream) {{ .SendName }}(event {{ .Endpoint.ResultRef }}) error {
    return s.{{ .SendWithContextName }}(s.ctx, event)
}

// {{ .SendWithContextName }} keeps request correlation while honoring this send's
// cancellation. Invalid service values return a server error before any bytes.
func (s *subscriptionStream) {{ .SendWithContextName }}(ctx context.Context, event {{ .Endpoint.ResultRef }}) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.closed {
        return goa.PermanentError("internal_error", "subscription source is closed")
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
        accepted := mcpruntime.SubscriptionFilter{}
        {{- if .Resources }}
        accepted.ResourceSubscriptions = make([]string, len(selected.{{ .AcknowledgedResources }}))
        for index, uri := range selected.{{ .AcknowledgedResources }} {
            accepted.ResourceSubscriptions[index] = string(uri)
        }
        {{- end }}
        {{- range .Tasks }}
        {{ .Prefix }}Accepted := make(map[string][]string)
        if selected.{{ $.SubscriptionSource.AcknowledgedTasks }} != nil {
            for _, id := range selected.{{ $.SubscriptionSource.AcknowledgedTasks }}.{{ .AcceptedField }} {
                nativeID := string(id)
                ids, requested := s.{{ .Prefix }}Requested[nativeID]
                if !requested { return goa.PermanentError("internal_error", "subscription source accepted an unrequested native job") }
                accepted.TaskIDs = append(accepted.TaskIDs, ids...)
                {{ .Prefix }}Accepted[nativeID] = ids
            }
        }
        {{- end }}
        if err := mcpruntime.AcknowledgeSubscription(sendContext, accepted); err != nil { return err }
        {{- range .Tasks }}
        s.{{ .Prefix }}Accepted = {{ .Prefix }}Accepted
        {{- end }}
        return nil
    {{- if .Tasks }}
    case {{ .TasksUpdatedKind }}:
        selected, _ := choice.AsTasksUpdated()
        {{- range .Tasks }}
        for _, id := range selected.{{ $.SubscriptionSource.UpdatedTasks }}.{{ .UpdatedField }} {
            ids, accepted := s.{{ .Prefix }}Accepted[string(id)]
            if !accepted { return goa.PermanentError("internal_error", "subscription source updated an unacknowledged native job") }
            for _, taskID := range ids {
                payload := &TasksGetPayload{TaskID:taskID, Meta:s.input.Meta}
                key, _, err := decodeTaskID(taskID)
                if err != nil { return err }
                switch key {
                {{- range .Tools }}
                case {{ quote .Key }}:
                    {{- range .ListenInputs }}
                    payload.{{ .Target }} = s.input.{{ .Source }}
                    {{- end }}
                {{- end }}
                }
                observation, err := s.adapter.readTask(sendContext, payload)
                if err != nil { return err }
                snapshot, err := {{ $.SubscriptionSource.TaskSnapshotEncode }}({{ if $.SubscriptionSource.TaskSnapshotPointer }}&{{ end }}observation.Outcome)
                if err != nil { return goa.PermanentError("internal_error", "%s", err.Error()) }
                if err := mcpruntime.ReportTaskChanged(sendContext, snapshot); err != nil { return err }
            }
        }
        {{- end }}
        return nil
    {{- end }}
    {{- if .Resources }}
    case {{ .UpdatedKind }}:
        selected, _ := choice.AsUpdated()
        return mcpruntime.ReportResourceUpdated(sendContext, string({{ .UpdatedURIValue }}))
    {{- end }}
    default:
        return goa.PermanentError("internal_error", "subscription source returned an undeclared event")
    }
}

{{- if .MustClose }}
// Close stops authored sends. The method's return completes the MCP response.
func (s *subscriptionStream) Close() error {
    s.close()
    return nil
}
{{- end }}

// close prevents sends through a source reference after its method finishes.
func (s *subscriptionStream) close() {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.closed = true
}
{{- end }}
