{{ comment "Broadcaster and publish helpers for server-initiated events" }}

// Publish sends an event to all event stream subscribers.
func (a *MCPAdapter) Publish(ev *EventsStreamResult) {
    if a == nil || a.broadcaster == nil {
        return
    }
    a.broadcaster.Publish(ev)
}

// PublishStatus is a convenience to publish a status_update message.
func (a *MCPAdapter) PublishStatus(ctx context.Context, typ string, message string, data any) {
    n := &mcpruntime.Notification{Type: typ, Message: &message, Data: data}
    s, err := mcpruntime.EncodeJSONToString(ctx, goahttp.ResponseEncoder, n)
    if err != nil {
        return
    }
    a.Publish(&EventsStreamResult{
        Content: []*ContentItem{ buildContentItem(a, s) },
    })
}


