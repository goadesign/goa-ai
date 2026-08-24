{{ comment "Notifications and events stream" }}

{{- range .Notifications }}
func (a *MCPAdapter) {{ .MCPMethodName }}(ctx context.Context, p {{ .PayloadRef }}) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    if p == nil || p.Type == "" {
        return goa.PermanentError("invalid_params", "Missing notification type")
    }
    notification := &mcpruntime.Notification{
        Type: p.Type,
        Message: p.Message,
        Data: p.Data,
    }
    s, err := mcpruntime.EncodeJSONToString(ctx, goahttp.ResponseEncoder, notification)
    if err != nil {
        return err
    }
    ev := &EventsStreamResult{
        Content: []*ContentItem{
            buildContentItem(a, s),
        },
    }
    a.Publish(ev)
    return nil
}
{{- end }}

func (a *MCPAdapter) EventsStream(ctx context.Context, stream EventsStreamServerStream) error {
    if !a.isInitialized() {
        return goa.PermanentError("internal_error", "Not initialized")
    }
    sub, err := a.broadcaster.Subscribe(ctx)
    if err != nil {
        return goa.PermanentError("internal_error", "Failed to subscribe to events: %v", err)
    }
    defer sub.Close()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case ev, ok := <-sub.C():
            if !ok {
                return nil
            }
            if err := stream.Send(ev.(*EventsStreamResult)); err != nil {
                return goa.PermanentError("internal_error", "Failed to send event: %v", err)
            }
        }
    }
}
