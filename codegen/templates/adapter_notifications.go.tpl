{{- if .Notifications }}
{{ comment "Notifications and events stream" }}

func (a *MCPAdapter) NotifyStatusUpdate(ctx context.Context, p *SendNotificationPayload) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    if p == nil || p.Type == "" {
        return goa.PermanentError("invalid_params", "Missing notification type")
    }
    m := map[string]any{"type": p.Type}
    if p.Message != nil {
        m["message"] = *p.Message
    }
    if p.Data != nil {
        m["data"] = p.Data
    }
    s, err := encodeJSONToString(ctx, m)
    if err != nil {
        return err
    }
    ev := &EventsStreamResult{
        Content: []*ContentItem{ buildContentItem(a, s) },
    }
    a.Publish(ev)
    return nil
}

func (a *MCPAdapter) EventsStream(ctx context.Context, stream EventsStreamServerStream) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    if a.broadcaster == nil {
        return goa.PermanentError("invalid_params", "No broadcaster configured")
    }
    sub, _ := a.broadcaster.Subscribe(ctx)
    defer sub.Close()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case ev, ok := <-sub.C():
            if !ok { return nil }
            if err := stream.Send(ctx, ev); err != nil { return err }
        }
    }
}
{{- end }}


