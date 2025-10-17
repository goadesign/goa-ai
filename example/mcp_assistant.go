package assistantapi

import (
	"context"
	"strings"

	mcpassistant "example.com/assistant/gen/mcp_assistant"
	"goa.design/clue/log"
)

// mcp_assistant service example implementation.
// The example methods log the requests and return zero values.
type mcpAssistantsrvc struct{}

// providedMCPOptions holds options set by the server main via flags.
var providedMCPOptions *mcpassistant.MCPAdapterOptions

// SetMCPAdapterOptions allows the server main to configure MCP adapter options
// without relying on environment variables.
func SetMCPAdapterOptions(o *mcpassistant.MCPAdapterOptions) {
	providedMCPOptions = o
}

// NewMcpAssistant returns the mcp_assistant service implementation.
func NewMcpAssistant() mcpassistant.Service {
	// Prefer options provided via flags; do not read environment variables
	var opts *mcpassistant.MCPAdapterOptions
	if providedMCPOptions != nil {
		opts = providedMCPOptions
	} else {
		opts = &mcpassistant.MCPAdapterOptions{}
	}
	// If no policy provided, apply a simple default for tests: deny reading system_info
	if opts != nil && len(opts.AllowedResourceNames) == 0 &&
		len(opts.DeniedResourceNames) == 0 &&
		len(opts.AllowedResourceURIs) == 0 &&
		len(opts.DeniedResourceURIs) == 0 {
		opts.DeniedResourceNames = []string{"system_info"}
	}
	return mcpassistant.NewMCPAdapter(NewAssistant(), &promptProvider{}, opts)
}

// SplitCSV splits a comma-separated list into trimmed items, ignoring empties.
func SplitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Initialize MCP session
func (s *mcpAssistantsrvc) Initialize(ctx context.Context, p *mcpassistant.InitializePayload) (res *mcpassistant.InitializeResult, err error) {
	log.Printf(ctx, "mcpAssistant.initialize")
	return &mcpassistant.InitializeResult{}, nil
}

// Ping the server
func (s *mcpAssistantsrvc) Ping(ctx context.Context) (res *mcpassistant.PingResult, err error) {
	res = &mcpassistant.PingResult{}
	log.Printf(ctx, "mcpAssistant.ping")
	return
}

// List available tools
func (s *mcpAssistantsrvc) ToolsList(ctx context.Context, p *mcpassistant.ToolsListPayload) (res *mcpassistant.ToolsListResult, err error) {
	log.Printf(ctx, "mcpAssistant.tools/list")
	return &mcpassistant.ToolsListResult{}, nil
}

// Call a tool
func (s *mcpAssistantsrvc) ToolsCall(ctx context.Context, p *mcpassistant.ToolsCallPayload, stream mcpassistant.ToolsCallServerStream) error {
	log.Printf(ctx, "mcpAssistant.tools/call")
	return nil
}

// List available resources
func (s *mcpAssistantsrvc) ResourcesList(ctx context.Context, p *mcpassistant.ResourcesListPayload) (res *mcpassistant.ResourcesListResult, err error) {
	log.Printf(ctx, "mcpAssistant.resources/list")
	return &mcpassistant.ResourcesListResult{}, nil
}

// Read a resource
func (s *mcpAssistantsrvc) ResourcesRead(ctx context.Context, p *mcpassistant.ResourcesReadPayload) (res *mcpassistant.ResourcesReadResult, err error) {
	log.Printf(ctx, "mcpAssistant.resources/read")
	return &mcpassistant.ResourcesReadResult{}, nil
}

// Subscribe to resource changes
func (s *mcpAssistantsrvc) ResourcesSubscribe(ctx context.Context, p *mcpassistant.ResourcesSubscribePayload) (err error) {
	log.Printf(ctx, "mcpAssistant.resources/subscribe")
	return
}

// Unsubscribe from resource changes
func (s *mcpAssistantsrvc) ResourcesUnsubscribe(ctx context.Context, p *mcpassistant.ResourcesUnsubscribePayload) (err error) {
	log.Printf(ctx, "mcpAssistant.resources/unsubscribe")
	return
}

// List available prompts
func (s *mcpAssistantsrvc) PromptsList(ctx context.Context, p *mcpassistant.PromptsListPayload) (res *mcpassistant.PromptsListResult, err error) {
	log.Printf(ctx, "mcpAssistant.prompts/list")
	return &mcpassistant.PromptsListResult{}, nil
}

// Get a prompt by name
func (s *mcpAssistantsrvc) PromptsGet(ctx context.Context, p *mcpassistant.PromptsGetPayload) (res *mcpassistant.PromptsGetResult, err error) {
	log.Printf(ctx, "mcpAssistant.prompts/get")
	return &mcpassistant.PromptsGetResult{}, nil
}

// Send status updates to client
func (s *mcpAssistantsrvc) NotifyStatusUpdate(ctx context.Context, p *mcpassistant.SendNotificationPayload) (err error) {
	log.Printf(ctx, "mcpAssistant.notify_status_update")
	return
}

// Subscribe to resource updates
func (s *mcpAssistantsrvc) Subscribe(ctx context.Context, p *mcpassistant.SubscribePayload) (res *mcpassistant.SubscribeResult, err error) {
	res = &mcpassistant.SubscribeResult{}
	log.Printf(ctx, "mcpAssistant.subscribe")
	return
}

// Unsubscribe from resource updates
func (s *mcpAssistantsrvc) Unsubscribe(ctx context.Context, p *mcpassistant.UnsubscribePayload) (res *mcpassistant.UnsubscribeResult, err error) {
	res = &mcpassistant.UnsubscribeResult{}
	log.Printf(ctx, "mcpAssistant.unsubscribe")
	return
}
