{{- if .Subscriptions }}
{{ comment "General subscriptions handling" }}

func (a *MCPAdapter) Subscribe(ctx context.Context, p *SubscribePayload) (*SubscribeResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "subscribe"})
    res := &SubscribeResult{Success: true}
    a.log(ctx, "response", map[string]any{"method": "subscribe"})
    return res, nil
}

func (a *MCPAdapter) Unsubscribe(ctx context.Context, p *UnsubscribePayload) (*UnsubscribeResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "unsubscribe"})
    res := &UnsubscribeResult{Success: true}
    a.log(ctx, "response", map[string]any{"method": "unsubscribe"})
    return res, nil
}
{{- end }}



