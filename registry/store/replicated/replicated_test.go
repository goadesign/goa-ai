package replicated

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/registry/store"
)

type fakeMap struct {
	mu      sync.RWMutex
	content map[string]string
}

func newFakeMap() *fakeMap {
	return &fakeMap{content: make(map[string]string)}
}

var _ Map = (*fakeMap)(nil)

func (m *fakeMap) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.content))
	for k := range m.content {
		out = append(out, k)
	}
	return out
}

func (m *fakeMap) Get(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.content[key]
	return v, ok
}

func (m *fakeMap) Set(ctx context.Context, key, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.content[key]
	m.content[key] = value
	return prev, nil
}

func (m *fakeMap) Delete(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.content[key]
	delete(m.content, key)
	return prev, nil
}

func TestStore_SaveGetDelete(t *testing.T) {
	ctx := context.Background()
	s := New(newFakeMap())

	ts := &genregistry.Toolset{
		Name:        "atlas_data.atlas.read",
		Description: strPtr("Atlas read tools"),
		Tags:        []string{"atlas", "read"},
		Tools: []*genregistry.ToolSchema{
			{
				Name:          "atlas.read.get_device_snapshot",
				PayloadSchema: []byte(`{"type":"object"}`),
				ResultSchema:  []byte(`{"type":"object"}`),
			},
		},
		StreamID:     "toolset:atlas_data.atlas.read:requests",
		RegisteredAt: "2025-12-23T00:00:00Z",
	}

	err := s.SaveToolset(ctx, ts)
	require.NoError(t, err)

	got, err := s.GetToolset(ctx, ts.Name)
	require.NoError(t, err)
	assert.Equal(t, ts.Name, got.Name)
	assert.Equal(t, ts.StreamID, got.StreamID)
	assert.Equal(t, ts.Tags, got.Tags)
	assert.Len(t, got.Tools, 1)
	assert.Equal(t, "atlas.read.get_device_snapshot", got.Tools[0].Name)

	err = s.DeleteToolset(ctx, ts.Name)
	require.NoError(t, err)

	_, err = s.GetToolset(ctx, ts.Name)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestStore_ListAndSearch(t *testing.T) {
	ctx := context.Background()
	s := New(newFakeMap())

	err := s.SaveToolset(ctx, &genregistry.Toolset{
		Name:        "todos.todos",
		Description: strPtr("Todos tools"),
		Tags:        []string{"todos"},
		Tools: []*genregistry.ToolSchema{
			{Name: "todos.update_todos", PayloadSchema: []byte(`{"type":"object"}`), ResultSchema: []byte(`{"type":"object"}`)},
		},
		StreamID:     "toolset:todos.todos:requests",
		RegisteredAt: "2025-12-23T00:00:00Z",
	})
	require.NoError(t, err)
	err = s.SaveToolset(ctx, &genregistry.Toolset{
		Name:        "atlas_data.atlas.read",
		Description: strPtr("Atlas read tools"),
		Tags:        []string{"atlas", "read"},
		Tools: []*genregistry.ToolSchema{
			{Name: "atlas.read.get_device_snapshot", PayloadSchema: []byte(`{"type":"object"}`), ResultSchema: []byte(`{"type":"object"}`)},
		},
		StreamID:     "toolset:atlas_data.atlas.read:requests",
		RegisteredAt: "2025-12-23T00:00:00Z",
	})
	require.NoError(t, err)

	all, err := s.ListToolsets(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	atlasOnly, err := s.ListToolsets(ctx, []string{"atlas"})
	require.NoError(t, err)
	assert.Len(t, atlasOnly, 1)
	assert.Equal(t, "atlas_data.atlas.read", atlasOnly[0].Name)

	readOnly, err := s.SearchToolsets(ctx, "snapshot")
	require.NoError(t, err)
	assert.Empty(t, readOnly, "search matches name/description/tags only")

	searchAtlas, err := s.SearchToolsets(ctx, "atlas")
	require.NoError(t, err)
	assert.Len(t, searchAtlas, 1)
	assert.Equal(t, "atlas_data.atlas.read", searchAtlas[0].Name)
}

func strPtr(v string) *string {
	out := v
	return &out
}
