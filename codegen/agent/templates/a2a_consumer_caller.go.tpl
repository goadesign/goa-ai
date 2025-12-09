package {{ .PackageName }}

import (
	"context"

	a2a "goa.design/goa-ai/runtime/a2a"
	httpclient "goa.design/goa-ai/runtime/a2a/httpclient"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
)

// NewCaller creates an A2A caller for the remote provider. Authentication and
// transport options are configured via httpclient.Option values.
func NewCaller(url string, opts ...httpclient.Option) (a2a.Caller, error) {
	return httpclient.New(url, opts...)
}

// Register registers the remote A2A provider with the agent runtime using the
// given Caller and the generated ProviderConfig.
func Register(ctx context.Context, rt *agentruntime.Runtime, caller a2a.Caller) error {
	return a2a.RegisterProvider(ctx, rt, caller, ProviderConfig)
}

// RegisterWithURL is a convenience helper that creates an A2A caller with the
// given URL and options, registers the provider, and returns the caller.
func RegisterWithURL(ctx context.Context, rt *agentruntime.Runtime, url string, opts ...httpclient.Option) (a2a.Caller, error) {
	caller, err := NewCaller(url, opts...)
	if err != nil {
		return nil, err
	}
	if err := Register(ctx, rt, caller); err != nil {
		return nil, err
	}
	return caller, nil
}


