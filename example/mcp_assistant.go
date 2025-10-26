package assistantapi

import (
	"context"
	"strings"

	mcpassistant "example.com/assistant/gen/mcp_assistant"
	mcpruntime "goa.design/goa-ai/features/mcp/runtime"
	goa "goa.design/goa/v3/pkg"
)

var providedMCPOptions *mcpassistant.MCPAdapterOptions

// SetMCPAdapterOptions allows tests or command-line setup code to override
// the default adapter behavior (resource policies, protocol flags, etc.).
func SetMCPAdapterOptions(opts *mcpassistant.MCPAdapterOptions) {
	providedMCPOptions = opts
}

// NewMcpAssistant wires the business service into the generated MCP adapter so
// JSON-RPC clients speak to the strongly typed service implementation. It also
// applies a sensible default policy (deny reading system info unless explicitly
// allowed) so integration tests can exercise resource filters.
func NewMcpAssistant() mcpassistant.Service {
	adapter := mcpassistant.NewMCPAdapter(NewAssistant(), &promptProvider{}, adapterOptions())
	return &adapterService{MCPAdapter: adapter}
}

// adapterService adapts the generated MCP adapter to the Service interface by
// translating the notify_status_update payload into the runtime notification
// format expected by the adapter’s advanced publishing path.
type adapterService struct {
	*mcpassistant.MCPAdapter
}

// NotifyStatusUpdate adapts the generated payload into the runtime notification
// struct used by the adapter so downstream runtime helpers can reuse encoding
// and telemetry logic.
func (s *adapterService) NotifyStatusUpdate(
	ctx context.Context,
	payload *mcpassistant.SendNotificationPayload,
) error {
	if payload == nil {
		return goa.PermanentError("invalid_params", "Missing notification payload")
	}
	if payload.Type == "" {
		return goa.PermanentError("invalid_params", "Missing notification type")
	}
	notification := &mcpruntime.Notification{
		Type:    payload.Type,
		Message: payload.Message,
		Data:    payload.Data,
	}
	return s.MCPAdapter.NotifyStatusUpdate(ctx, notification)
}

func adapterOptions() *mcpassistant.MCPAdapterOptions {
	if providedMCPOptions != nil {
		return providedMCPOptions
	}
	opts := &mcpassistant.MCPAdapterOptions{}
	if len(opts.AllowedResourceNames) == 0 &&
		len(opts.DeniedResourceNames) == 0 &&
		len(opts.AllowedResourceURIs) == 0 &&
		len(opts.DeniedResourceURIs) == 0 {
		opts.DeniedResourceNames = []string{"system_info"}
	}
	opts.AllowedResourceNames = normalizeNames(opts.AllowedResourceNames)
	opts.DeniedResourceNames = normalizeNames(opts.DeniedResourceNames)
	return opts
}

func normalizeNames(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
