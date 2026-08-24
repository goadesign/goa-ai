{{ comment "Notifications and events stream" }}

func (a *MCPAdapter) NotifyStatusUpdate(ctx context.Context, n *mcpruntime.Notification) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    if n == nil || n.Type == "" {
        return goa.PermanentError("invalid_params", "Missing notification type")
    }
    s, err := mcpruntime.EncodeJSONToString(ctx, goahttp.ResponseEncoder, n)
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
            // Ensure published events implement the generated EventsStreamEvent marker.
            evt, ok := ev.(EventsStreamEvent)
            if !ok {
                continue
            }
            if err := stream.Send(ctx, evt); err != nil { 
                return goa.PermanentError("internal_error", "Failed to send event: %v", err)
            }
        }
    }
}


