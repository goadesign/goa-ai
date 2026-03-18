package registry

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestToolsetCatalogSaveGetDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cat := newToolsetCatalog(newTestCatalogMap())

	toolset := &genregistry.Toolset{
		Name:         "atlas.read",
		Tags:         []string{"atlas", "read"},
		RegisteredAt: "2026-03-16T12:00:00Z",
		Tools: []*genregistry.ToolSchema{
			{
				Name:          "atlas.read.get_time_series",
				PayloadSchema: []byte(`{"type":"object"}`),
				ResultSchema:  []byte(`{"type":"object"}`),
			},
		},
	}

	require.NoError(t, cat.SaveToolset(ctx, toolset))

	got, err := cat.GetToolset(ctx, toolset.Name)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, toolset.Name, got.Name)
	assert.Equal(t, toolset.RegisteredAt, got.RegisteredAt)
	assert.Equal(t, toolset.Tags, got.Tags)

	require.NoError(t, cat.DeleteToolset(ctx, toolset.Name))

	_, err = cat.GetToolset(ctx, toolset.Name)
	require.ErrorIs(t, err, errToolsetNotFound)

	err = cat.DeleteToolset(ctx, toolset.Name)
	require.ErrorIs(t, err, errToolsetNotFound)
}

func TestToolsetCatalogSaveRotatesRegistrationToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backingMap := newTestCatalogMap()
	cat := newToolsetCatalog(backingMap)
	toolset := testCatalogToolset("atlas.read", "Atlas reads", []string{"atlas", "read"})

	require.NoError(t, cat.SaveToolset(ctx, toolset))
	firstRaw, ok := backingMap.Get(toolsetCatalogKey(toolset.Name))
	require.True(t, ok)
	firstEntry, err := parseCatalogEntry(toolset.Name, firstRaw)
	require.NoError(t, err)

	require.NoError(t, cat.SaveToolset(ctx, toolset))
	secondRaw, ok := backingMap.Get(toolsetCatalogKey(toolset.Name))
	require.True(t, ok)
	secondEntry, err := parseCatalogEntry(toolset.Name, secondRaw)
	require.NoError(t, err)

	assert.NotEmpty(t, firstEntry.RegistrationToken)
	assert.NotEmpty(t, secondEntry.RegistrationToken)
	assert.NotEqual(t, firstEntry.RegistrationToken, secondEntry.RegistrationToken)
	assert.Equal(t, toolset.Name, secondEntry.Toolset.Name)
	assert.Equal(t, toolset.RegisteredAt, secondEntry.Toolset.RegisteredAt)
}

func TestToolsetCatalogListToolsetsFiltersTags(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cat := newToolsetCatalog(newTestCatalogMap())
	require.NoError(t, cat.SaveToolset(ctx, testCatalogToolset("atlas.read", "Atlas reads", []string{"atlas", "read"})))
	require.NoError(t, cat.SaveToolset(ctx, testCatalogToolset("atlas.write", "Atlas writes", []string{"atlas", "write"})))
	require.NoError(t, cat.SaveToolset(ctx, testCatalogToolset("grafana.read", "Grafana reads", []string{"grafana", "read"})))

	got, err := cat.ListToolsets(ctx, []string{"atlas", "read"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "atlas.read", got[0].Name)
}

func TestToolsetCatalogSearchToolsetsMatchesNameDescriptionAndTags(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cat := newToolsetCatalog(newTestCatalogMap())
	require.NoError(t, cat.SaveToolset(ctx, testCatalogToolset("atlas.read", "Reads Atlas time series", []string{"atlas", "signals"})))
	require.NoError(t, cat.SaveToolset(ctx, testCatalogToolset("grafana.read", "Reads dashboards", []string{"dashboards"})))

	t.Run("matches name", func(t *testing.T) {
		got, err := cat.SearchToolsets(ctx, "atlas")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "atlas.read", got[0].Name)
	})

	t.Run("matches description", func(t *testing.T) {
		got, err := cat.SearchToolsets(ctx, "dashboards")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "grafana.read", got[0].Name)
	})

	t.Run("matches tags case insensitively", func(t *testing.T) {
		got, err := cat.SearchToolsets(ctx, "SIGNALS")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "atlas.read", got[0].Name)
	})
}

type testCatalogMap struct {
	mu     sync.RWMutex
	values map[string]string
}

func newTestCatalogMap() *testCatalogMap {
	return &testCatalogMap{values: make(map[string]string)}
}

func (m *testCatalogMap) Delete(ctx context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.values[key]
	delete(m.values, key)
	return prev, nil
}

func (m *testCatalogMap) Get(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.values[key]
	return val, ok
}

func (m *testCatalogMap) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.values))
	for key := range m.values {
		keys = append(keys, key)
	}
	return keys
}

func (m *testCatalogMap) Set(ctx context.Context, key, value string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.values[key]
	m.values[key] = value
	return prev, nil
}

func testCatalogToolset(name string, description string, tags []string) *genregistry.Toolset {
	return &genregistry.Toolset{
		Name:         name,
		Description:  &description,
		Tags:         tags,
		RegisteredAt: "2026-03-16T12:00:00Z",
		Tools: []*genregistry.ToolSchema{
			{
				Name:          name + ".tool",
				PayloadSchema: []byte(`{"type":"object"}`),
				ResultSchema:  []byte(`{"type":"object"}`),
			},
		},
	}
}
