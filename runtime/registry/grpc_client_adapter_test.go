package registry

import (
	"context"
	"errors"
	"testing"

	registrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	"google.golang.org/grpc"
)

const testToolsetName = "test-toolset"

// mockGRPCRegistryClient implements registrypb.RegistryClient for testing.
type mockGRPCRegistryClient struct {
	listToolsetsResp *registrypb.ListToolsetsResponse
	listToolsetsErr  error
	getToolsetResp   *registrypb.GetToolsetResponse
	getToolsetErr    error
	searchResp       *registrypb.SearchResponse
	searchErr        error
}

func (m *mockGRPCRegistryClient) Register(_ context.Context, _ *registrypb.RegisterRequest, _ ...grpc.CallOption) (*registrypb.RegisterResponse, error) {
	return nil, nil
}

func (m *mockGRPCRegistryClient) Unregister(_ context.Context, _ *registrypb.UnregisterRequest, _ ...grpc.CallOption) (*registrypb.UnregisterResponse, error) {
	return nil, nil
}

func (m *mockGRPCRegistryClient) Pong(_ context.Context, _ *registrypb.PongRequest, _ ...grpc.CallOption) (*registrypb.PongResponse, error) {
	return nil, nil
}

func (m *mockGRPCRegistryClient) ListToolsets(_ context.Context, _ *registrypb.ListToolsetsRequest, _ ...grpc.CallOption) (*registrypb.ListToolsetsResponse, error) {
	return m.listToolsetsResp, m.listToolsetsErr
}

func (m *mockGRPCRegistryClient) GetToolset(_ context.Context, _ *registrypb.GetToolsetRequest, _ ...grpc.CallOption) (*registrypb.GetToolsetResponse, error) {
	return m.getToolsetResp, m.getToolsetErr
}

func (m *mockGRPCRegistryClient) Search(_ context.Context, _ *registrypb.SearchRequest, _ ...grpc.CallOption) (*registrypb.SearchResponse, error) {
	return m.searchResp, m.searchErr
}

func (m *mockGRPCRegistryClient) CallTool(_ context.Context, _ *registrypb.CallToolRequest, _ ...grpc.CallOption) (*registrypb.CallToolResponse, error) {
	return nil, nil
}

// TestGRPCClientAdapter_ListToolsets tests the ListToolsets method.
// **Validates: Requirements 11.1**
func TestGRPCClientAdapter_ListToolsets(t *testing.T) {
	ctx := context.Background()

	t.Run("returns toolsets from gRPC client", func(t *testing.T) {
		desc := "Test toolset"
		version := "1.0.0"
		mock := &mockGRPCRegistryClient{
			listToolsetsResp: &registrypb.ListToolsetsResponse{
				Toolsets: []*registrypb.ToolsetInfo{
					{
						Name:        testToolsetName,
						Description: &desc,
						Version:     &version,
						Tags:        []string{"tag1", "tag2"},
						ToolCount:   3,
					},
				},
			},
		}

		adapter := NewGRPCClientAdapter(mock)
		toolsets, err := adapter.ListToolsets(ctx)
		if err != nil {
			t.Fatalf("ListToolsets failed: %v", err)
		}
		if len(toolsets) != 1 {
			t.Fatalf("expected 1 toolset, got %d", len(toolsets))
		}
		if toolsets[0].Name != testToolsetName {
			t.Errorf("Name: got %q, want %q", toolsets[0].Name, testToolsetName)
		}
		if toolsets[0].Description != desc {
			t.Errorf("Description: got %q, want %q", toolsets[0].Description, desc)
		}
		if toolsets[0].Version != version {
			t.Errorf("Version: got %q, want %q", toolsets[0].Version, version)
		}
		if len(toolsets[0].Tags) != 2 {
			t.Errorf("Tags: got %d, want 2", len(toolsets[0].Tags))
		}
	})

	t.Run("returns empty list when no toolsets", func(t *testing.T) {
		mock := &mockGRPCRegistryClient{
			listToolsetsResp: &registrypb.ListToolsetsResponse{
				Toolsets: nil,
			},
		}

		adapter := NewGRPCClientAdapter(mock)
		toolsets, err := adapter.ListToolsets(ctx)
		if err != nil {
			t.Fatalf("ListToolsets failed: %v", err)
		}
		if len(toolsets) != 0 {
			t.Errorf("expected 0 toolsets, got %d", len(toolsets))
		}
	})

	t.Run("propagates errors from gRPC client", func(t *testing.T) {
		mock := &mockGRPCRegistryClient{
			listToolsetsErr: errors.New("connection failed"),
		}

		adapter := NewGRPCClientAdapter(mock)
		_, err := adapter.ListToolsets(ctx)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestGRPCClientAdapter_GetToolset tests the GetToolset method.
// **Validates: Requirements 11.1**
func TestGRPCClientAdapter_GetToolset(t *testing.T) {
	ctx := context.Background()

	t.Run("returns toolset schema from gRPC client", func(t *testing.T) {
		desc := "Test toolset"
		version := "1.0.0"
		toolDesc := "A test tool"
		mock := &mockGRPCRegistryClient{
			getToolsetResp: &registrypb.GetToolsetResponse{
				Name:        testToolsetName,
				Description: &desc,
				Version:     &version,
				Tags:        []string{"tag1"},
				Tools: []*registrypb.ToolSchema{
					{
						Name:          "test-tool",
						Description:   &toolDesc,
						PayloadSchema: []byte(`{"type":"object"}`),
					},
				},
			},
		}

		adapter := NewGRPCClientAdapter(mock)
		schema, err := adapter.GetToolset(ctx, testToolsetName)
		if err != nil {
			t.Fatalf("GetToolset failed: %v", err)
		}
		if schema.Name != testToolsetName {
			t.Errorf("Name: got %q, want %q", schema.Name, testToolsetName)
		}
		if schema.Description != desc {
			t.Errorf("Description: got %q, want %q", schema.Description, desc)
		}
		if len(schema.Tools) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(schema.Tools))
		}
		if schema.Tools[0].Name != "test-tool" {
			t.Errorf("Tool Name: got %q, want %q", schema.Tools[0].Name, "test-tool")
		}
		if string(schema.Tools[0].PayloadSchema) != `{"type":"object"}` {
			t.Errorf("Tool PayloadSchema: got %q", string(schema.Tools[0].PayloadSchema))
		}
	})

	t.Run("propagates errors from gRPC client", func(t *testing.T) {
		mock := &mockGRPCRegistryClient{
			getToolsetErr: errors.New("not found"),
		}

		adapter := NewGRPCClientAdapter(mock)
		_, err := adapter.GetToolset(ctx, "unknown")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestGRPCClientAdapter_Search tests the Search method.
// **Validates: Requirements 11.1**
func TestGRPCClientAdapter_Search(t *testing.T) {
	ctx := context.Background()

	t.Run("returns search results from gRPC client", func(t *testing.T) {
		desc := "A matching toolset"
		mock := &mockGRPCRegistryClient{
			searchResp: &registrypb.SearchResponse{
				Toolsets: []*registrypb.ToolsetInfo{
					{
						Name:        "matching-toolset",
						Description: &desc,
						Tags:        []string{"search", "test"},
					},
				},
			},
		}

		adapter := NewGRPCClientAdapter(mock)
		results, err := adapter.Search(ctx, "matching")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Name != "matching-toolset" {
			t.Errorf("Name: got %q, want %q", results[0].Name, "matching-toolset")
		}
		if results[0].Type != "toolset" {
			t.Errorf("Type: got %q, want %q", results[0].Type, "toolset")
		}
		if len(results[0].Tags) != 2 {
			t.Errorf("Tags: got %d, want 2", len(results[0].Tags))
		}
	})

	t.Run("returns empty results when no matches", func(t *testing.T) {
		mock := &mockGRPCRegistryClient{
			searchResp: &registrypb.SearchResponse{
				Toolsets: nil,
			},
		}

		adapter := NewGRPCClientAdapter(mock)
		results, err := adapter.Search(ctx, "nomatch")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("propagates errors from gRPC client", func(t *testing.T) {
		mock := &mockGRPCRegistryClient{
			searchErr: errors.New("search failed"),
		}

		adapter := NewGRPCClientAdapter(mock)
		_, err := adapter.Search(ctx, "query")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestGRPCClientAdapter_ImplementsInterface verifies the adapter implements RegistryClient.
// **Validates: Requirements 11.1**
func TestGRPCClientAdapter_ImplementsInterface(t *testing.T) {
	// This test verifies at runtime that the adapter implements the interface.
	// The compile-time check is in the adapter file itself.
	var _ RegistryClient = (*GRPCClientAdapter)(nil)
}

// TestGRPCClientAdapter_IntegrationWithManager tests that the adapter works with Manager.
// **Validates: Requirements 11.2**
func TestGRPCClientAdapter_IntegrationWithManager(t *testing.T) {
	ctx := context.Background()

	desc := "Integration test toolset"
	version := "2.0.0"
	toolDesc := "Integration tool"
	mock := &mockGRPCRegistryClient{
		listToolsetsResp: &registrypb.ListToolsetsResponse{
			Toolsets: []*registrypb.ToolsetInfo{
				{
					Name:        "integration-toolset",
					Description: &desc,
					Version:     &version,
					Tags:        []string{"integration"},
				},
			},
		},
		getToolsetResp: &registrypb.GetToolsetResponse{
			Name:        "integration-toolset",
			Description: &desc,
			Version:     &version,
			Tools: []*registrypb.ToolSchema{
				{
					Name:          "integration-tool",
					Description:   &toolDesc,
					PayloadSchema: []byte(`{"type":"string"}`),
				},
			},
		},
		searchResp: &registrypb.SearchResponse{
			Toolsets: []*registrypb.ToolsetInfo{
				{
					Name:        "integration-toolset",
					Description: &desc,
				},
			},
		},
	}

	adapter := NewGRPCClientAdapter(mock)
	manager := NewManager()
	manager.AddRegistry(testRegistryName, adapter, RegistryConfig{})

	// Test DiscoverToolset through manager
	schema, err := manager.DiscoverToolset(ctx, testRegistryName, "integration-toolset")
	if err != nil {
		t.Fatalf("DiscoverToolset failed: %v", err)
	}
	if schema.Name != "integration-toolset" {
		t.Errorf("Name: got %q, want %q", schema.Name, "integration-toolset")
	}
	if schema.Origin != testRegistryName {
		t.Errorf("Origin: got %q, want %q", schema.Origin, testRegistryName)
	}

	// Test Search through manager
	results, err := manager.Search(ctx, "integration")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Origin != testRegistryName {
		t.Errorf("Origin: got %q, want %q", results[0].Origin, testRegistryName)
	}
}
