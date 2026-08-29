// Package registry provides client-side catalog discovery across one or more
// registry services. This file projects the generated Goa registry service
// results into the stable resource types consumed by Manager.
package registry

import (
	"context"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// Client exposes a generated Goa registry service client through the discovery
// contract consumed by Manager. The generated client owns transport payloads
// and errors; Client adds only the resource projection needed by federation,
// caching, and cross-registry search.
type Client struct {
	client *genregistry.Client
}

// NewClient creates a discovery client backed by Goa's generated registry
// service client. Callers construct the generated client with their chosen Goa
// transport and retain ownership of the underlying connection.
func NewClient(client *genregistry.Client) *Client {
	return &Client{client: client}
}

// ListToolsets returns the registry's current toolset catalog.
func (c *Client) ListToolsets(ctx context.Context) ([]*ToolsetInfo, error) {
	result, err := c.client.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
	if err != nil {
		return nil, err
	}
	toolsets := make([]*ToolsetInfo, len(result.Toolsets))
	for i, toolset := range result.Toolsets {
		toolsets[i] = toolsetInfoFromGenerated(toolset)
	}
	return toolsets, nil
}

// GetToolset returns one complete toolset schema from the registry.
func (c *Client) GetToolset(ctx context.Context, name string) (*ToolsetSchema, error) {
	result, err := c.client.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: name})
	if err != nil {
		return nil, err
	}
	return toolsetSchemaFromGenerated(result), nil
}

// Search returns the registry's toolsets matching query.
func (c *Client) Search(ctx context.Context, query string) ([]*SearchResult, error) {
	result, err := c.client.Search(ctx, &genregistry.SearchPayload{Query: query})
	if err != nil {
		return nil, err
	}
	toolsets := make([]*SearchResult, len(result.Toolsets))
	for i, toolset := range result.Toolsets {
		toolsets[i] = searchResultFromGenerated(toolset)
	}
	return toolsets, nil
}

// toolsetInfoFromGenerated retains every catalog field returned by the Goa
// service while converting optional generated values into the manager's value
// representation.
func toolsetInfoFromGenerated(toolset *genregistry.ToolsetInfo) *ToolsetInfo {
	info := &ToolsetInfo{
		ID:           toolset.Name,
		Name:         toolset.Name,
		Tags:         append([]string(nil), toolset.Tags...),
		ToolCount:    toolset.ToolCount,
		RegisteredAt: toolset.RegisteredAt,
	}
	if toolset.Description != nil {
		info.Description = *toolset.Description
	}
	if toolset.Version != nil {
		info.Version = string(*toolset.Version)
	}
	return info
}

// toolsetSchemaFromGenerated retains the complete payload, result, and sidecar
// schemas returned by the Goa service for later cache reads.
func toolsetSchemaFromGenerated(toolset *genregistry.Toolset) *ToolsetSchema {
	schema := &ToolsetSchema{
		ID:           toolset.Name,
		Name:         toolset.Name,
		Tags:         append([]string(nil), toolset.Tags...),
		RegisteredAt: toolset.RegisteredAt,
		Tools:        make([]*ToolSchema, len(toolset.Tools)),
	}
	if toolset.Description != nil {
		schema.Description = *toolset.Description
	}
	if toolset.Version != nil {
		schema.Version = string(*toolset.Version)
	}
	for i, tool := range toolset.Tools {
		schema.Tools[i] = toolSchemaFromGenerated(tool)
	}
	return schema
}

// toolSchemaFromGenerated copies the registry-owned JSON schemas so manager
// caches cannot alias mutable generated result slices.
func toolSchemaFromGenerated(tool *genregistry.ToolSchema) *ToolSchema {
	schema := &ToolSchema{
		Name:          tool.Name,
		Tags:          append([]string(nil), tool.Tags...),
		PayloadSchema: append([]byte(nil), tool.PayloadSchema...),
		ResultSchema:  append([]byte(nil), tool.ResultSchema...),
		SidecarSchema: append([]byte(nil), tool.SidecarSchema...),
	}
	if tool.Description != nil {
		schema.Description = *tool.Description
	}
	return schema
}

// searchResultFromGenerated converts one service search hit into the manager's
// cross-registry result. Manager adds the origin registry after this call.
func searchResultFromGenerated(toolset *genregistry.ToolsetInfo) *SearchResult {
	info := toolsetInfoFromGenerated(toolset)
	return &SearchResult{
		ID:           info.ID,
		Name:         info.Name,
		Description:  info.Description,
		Type:         "toolset",
		Tags:         info.Tags,
		Version:      info.Version,
		ToolCount:    info.ToolCount,
		RegisteredAt: info.RegisteredAt,
	}
}

var _ RegistryClient = (*Client)(nil)
