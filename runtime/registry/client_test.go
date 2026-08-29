package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestClientProjectsGeneratedDiscoveryResults(t *testing.T) {
	t.Parallel()

	const changed = "changed"
	description := "Catalog lookup tools"
	version := genregistry.SemVer("1.2.3")
	listed := &genregistry.ToolsetInfo{
		Name:         "catalog.lookup",
		Description:  &description,
		Version:      &version,
		Tags:         []string{"catalog"},
		ToolCount:    3,
		RegisteredAt: "2026-08-28T20:00:00Z",
	}
	found := &genregistry.Toolset{
		Name:         "catalog.lookup",
		Description:  &description,
		Version:      &version,
		Tags:         []string{"catalog"},
		RegisteredAt: "2026-08-28T20:00:00Z",
		Tools: []*genregistry.ToolSchema{{
			Name:          "catalog.lookup.get",
			Description:   &description,
			Tags:          []string{"read"},
			PayloadSchema: []byte(`{"type":"object"}`),
			ResultSchema:  []byte(`{"type":"array"}`),
			SidecarSchema: []byte(`{"type":"object"}`),
		}},
	}
	searched := &genregistry.ToolsetInfo{
		Name:         "catalog.lookup",
		Description:  &description,
		Version:      &version,
		Tags:         []string{"catalog"},
		ToolCount:    3,
		RegisteredAt: "2026-08-28T20:00:00Z",
	}
	serviceClient := &genregistry.Client{
		ListToolsetsEndpoint: func(_ context.Context, payload any) (any, error) {
			assert.Equal(t, &genregistry.ListToolsetsPayload{}, payload)
			return &genregistry.ListToolsetsResult{Toolsets: []*genregistry.ToolsetInfo{listed}}, nil
		},
		GetToolsetEndpoint: func(_ context.Context, payload any) (any, error) {
			assert.Equal(t, &genregistry.GetToolsetPayload{Name: "catalog.lookup"}, payload)
			return found, nil
		},
		SearchEndpoint: func(_ context.Context, payload any) (any, error) {
			assert.Equal(t, &genregistry.SearchPayload{Query: "catalogs"}, payload)
			return &genregistry.SearchResult{Toolsets: []*genregistry.ToolsetInfo{searched}}, nil
		},
	}
	client := NewClient(serviceClient)

	toolsets, err := client.ListToolsets(context.Background())
	require.NoError(t, err)
	require.Len(t, toolsets, 1)
	assert.Equal(t, "catalog.lookup", toolsets[0].ID)
	assert.Equal(t, "catalog.lookup", toolsets[0].Name)
	assert.Equal(t, "1.2.3", toolsets[0].Version)
	assert.Equal(t, 3, toolsets[0].ToolCount)
	assert.Equal(t, "2026-08-28T20:00:00Z", toolsets[0].RegisteredAt)

	toolset, err := client.GetToolset(context.Background(), "catalog.lookup")
	require.NoError(t, err)
	assert.Equal(t, "catalog.lookup", toolset.ID)
	assert.Equal(t, []string{"catalog"}, toolset.Tags)
	assert.Equal(t, "2026-08-28T20:00:00Z", toolset.RegisteredAt)
	require.Len(t, toolset.Tools, 1)
	assert.JSONEq(t, `{"type":"object"}`, string(toolset.Tools[0].PayloadSchema))
	assert.JSONEq(t, `{"type":"array"}`, string(toolset.Tools[0].ResultSchema))
	assert.JSONEq(t, `{"type":"object"}`, string(toolset.Tools[0].SidecarSchema))

	results, err := client.Search(context.Background(), "catalogs")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "catalog.lookup", results[0].ID)
	assert.Equal(t, "toolset", results[0].Type)
	assert.Equal(t, "catalog.lookup", results[0].Name)
	assert.Equal(t, "1.2.3", results[0].Version)
	assert.Equal(t, 3, results[0].ToolCount)
	assert.Equal(t, "2026-08-28T20:00:00Z", results[0].RegisteredAt)

	listed.Tags[0] = changed
	found.Tags[0] = changed
	found.Tools[0].Tags[0] = changed
	found.Tools[0].PayloadSchema[0] = '['
	searched.Tags[0] = changed
	assert.Equal(t, []string{"catalog"}, toolsets[0].Tags)
	assert.Equal(t, []string{"catalog"}, toolset.Tags)
	assert.Equal(t, []string{"read"}, toolset.Tools[0].Tags)
	assert.JSONEq(t, `{"type":"object"}`, string(toolset.Tools[0].PayloadSchema))
	assert.Equal(t, []string{"catalog"}, results[0].Tags)
}

func TestClientPropagatesGeneratedServiceErrors(t *testing.T) {
	t.Parallel()

	serviceErr := errors.New("registry unavailable")
	client := NewClient(&genregistry.Client{
		ListToolsetsEndpoint: func(context.Context, any) (any, error) {
			return nil, serviceErr
		},
		GetToolsetEndpoint: func(context.Context, any) (any, error) {
			return nil, serviceErr
		},
		SearchEndpoint: func(context.Context, any) (any, error) {
			return nil, serviceErr
		},
	})

	_, err := client.ListToolsets(context.Background())
	require.ErrorIs(t, err, serviceErr)
	_, err = client.GetToolset(context.Background(), "catalog.lookup")
	require.ErrorIs(t, err, serviceErr)
	_, err = client.Search(context.Background(), "catalogs")
	require.ErrorIs(t, err, serviceErr)
}

func TestClientWorksWithManager(t *testing.T) {
	t.Parallel()

	description := "Catalog tools"
	serviceClient := &genregistry.Client{
		SearchEndpoint: func(context.Context, any) (any, error) {
			return &genregistry.SearchResult{Toolsets: []*genregistry.ToolsetInfo{{
				Name:        "catalog.lookup",
				Description: &description,
			}}}, nil
		},
	}
	manager := NewManager()
	manager.AddRegistry("primary", NewClient(serviceClient), RegistryConfig{})

	results, err := manager.Search(context.Background(), "catalogs")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "primary", results[0].Origin)
}
