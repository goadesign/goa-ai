// Package registry provides client-side components for agents to consume
// the internal tool registry.
//
// This package is embedded into agent runtimes and provides:
//
//   - RegistryClient interface — abstraction for registry communication
//   - GRPCClientAdapter — wraps generated gRPC client to implement RegistryClient
//   - Manager — coordinates multiple registry connections and tool discovery
//
// For the server-side registry implementation that runs as a standalone
// service, see the registry package (goa.design/goa-ai/registry).
package registry

import (
	"context"

	registrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
)

// GRPCClientAdapter wraps a generated gRPC registry client and implements
// the RegistryClient interface for use with the runtime Manager.
type GRPCClientAdapter struct {
	client registrypb.RegistryClient
}

// NewGRPCClientAdapter creates a new adapter that wraps the generated gRPC
// client and implements the RegistryClient interface.
func NewGRPCClientAdapter(client registrypb.RegistryClient) *GRPCClientAdapter {
	return &GRPCClientAdapter{client: client}
}

// ListToolsets returns all available toolsets from the registry.
func (a *GRPCClientAdapter) ListToolsets(ctx context.Context) ([]*ToolsetInfo, error) {
	resp, err := a.client.ListToolsets(ctx, &registrypb.ListToolsetsRequest{})
	if err != nil {
		return nil, err
	}
	return convertToolsetInfoList(resp.GetToolsets()), nil
}

// GetToolset retrieves the full schema for a specific toolset.
func (a *GRPCClientAdapter) GetToolset(ctx context.Context, name string) (*ToolsetSchema, error) {
	resp, err := a.client.GetToolset(ctx, &registrypb.GetToolsetRequest{Name: name})
	if err != nil {
		return nil, err
	}
	return convertGetToolsetResponse(resp), nil
}

// Search performs a keyword search on the registry.
func (a *GRPCClientAdapter) Search(ctx context.Context, query string) ([]*SearchResult, error) {
	resp, err := a.client.Search(ctx, &registrypb.SearchRequest{Query: query})
	if err != nil {
		return nil, err
	}
	return convertSearchResults(resp.GetToolsets()), nil
}

// convertToolsetInfoList converts protobuf ToolsetInfo list to runtime ToolsetInfo list.
func convertToolsetInfoList(pbToolsets []*registrypb.ToolsetInfo) []*ToolsetInfo {
	if len(pbToolsets) == 0 {
		return nil
	}
	result := make([]*ToolsetInfo, len(pbToolsets))
	for i, pb := range pbToolsets {
		result[i] = &ToolsetInfo{
			Name:        pb.GetName(),
			Description: pb.GetDescription(),
			Version:     pb.GetVersion(),
			Tags:        pb.GetTags(),
		}
	}
	return result
}

// convertGetToolsetResponse converts protobuf GetToolsetResponse to runtime ToolsetSchema.
func convertGetToolsetResponse(pb *registrypb.GetToolsetResponse) *ToolsetSchema {
	tools := make([]*ToolSchema, len(pb.GetTools()))
	for i, t := range pb.GetTools() {
		tools[i] = &ToolSchema{
			Name:        t.GetName(),
			Description: t.GetDescription(),
			InputSchema: t.GetInputSchema(),
		}
	}
	return &ToolsetSchema{
		Name:        pb.GetName(),
		Description: pb.GetDescription(),
		Version:     pb.GetVersion(),
		Tools:       tools,
	}
}

// convertSearchResults converts protobuf ToolsetInfo list to runtime SearchResult list.
func convertSearchResults(pbToolsets []*registrypb.ToolsetInfo) []*SearchResult {
	if len(pbToolsets) == 0 {
		return nil
	}
	result := make([]*SearchResult, len(pbToolsets))
	for i, pb := range pbToolsets {
		result[i] = &SearchResult{
			Name:        pb.GetName(),
			Description: pb.GetDescription(),
			Type:        "toolset",
			Tags:        pb.GetTags(),
		}
	}
	return result
}

// Compile-time assertion that GRPCClientAdapter implements RegistryClient.
var _ RegistryClient = (*GRPCClientAdapter)(nil)
