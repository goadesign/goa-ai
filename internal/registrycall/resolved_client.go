// Package registrycall submits saved registration selections without replacing
// their token or operation when a provider asks the caller to retry admission.
package registrycall

import (
	"context"
	"fmt"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type resolvedRegistryClient struct {
	client    *genregistry.Client
	token     string
	operation string
}

// NewResolvedClient uses the saved token and provider operation for both initial
// admission and overload retries, so replacement registrations receive no calls.
func NewResolvedClient(client *genregistry.Client, token, operation string) Client {
	return resolvedRegistryClient{client: client, token: token, operation: operation}
}

// CallTool admits only the registration whose definition produced the call.
func (c resolvedRegistryClient) CallTool(ctx context.Context, payload *genregistry.CallToolPayload) (*genregistry.CallToolResult, error) {
	result, err := c.client.CallResolvedTool(ctx, &genregistry.CallResolvedToolPayload{
		ExpectedRegistrationToken: c.token,
		Toolset:                   payload.Toolset,
		Tool:                      c.operation,
		PayloadJSON:               payload.PayloadJSON,
		WireProtocolVersion:       payload.WireProtocolVersion,
		Meta:                      payload.Meta,
	})
	if err != nil {
		return nil, err
	}
	if result.RegistrationToken != c.token {
		return nil, fmt.Errorf("registry admitted a different registration for tool %q", payload.Tool)
	}
	return result, nil
}

// RetryTool resumes the original call's registry-owned overload event.
// It cannot replace the selected registration or start a new call identity.
func (c resolvedRegistryClient) RetryTool(ctx context.Context, payload *genregistry.RetryToolPayload) (*genregistry.CallToolResult, error) {
	if payload.ExpectedRegistrationToken != c.token {
		return nil, fmt.Errorf("registry retry token differs from the selected registration")
	}
	return c.CallTool(ctx, &genregistry.CallToolPayload{
		Toolset:             payload.Toolset,
		Tool:                payload.Tool,
		PayloadJSON:         payload.PayloadJSON,
		WireProtocolVersion: payload.WireProtocolVersion,
		Meta:                payload.Meta,
	})
}
