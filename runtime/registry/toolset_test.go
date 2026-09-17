// Package registry tests discovery as an application startup boundary.
// Accepted schemas must retain their validation and ownership after the source
// catalog changes; incomplete definitions must fail before agent registration.
package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDiscoveredToolsetOwnsSchemasAndCodecs(t *testing.T) {
	t.Parallel()

	source := discoveredSchema()
	set, err := NewToolset(source)
	require.NoError(t, err)
	assert.Equal(t, "lookup", set.Name())
	assert.Equal(t, "1.2.3", set.Version())
	assert.Equal(t, []tools.Ident{"lookup.find"}, set.Names())
	source.Tools[0].PayloadSchema[0] = '['
	source.Tools[0].Tags[0] = "changed"

	spec, ok := set.Spec("lookup.find")
	require.True(t, ok)
	assert.Equal(t, []string{"read"}, spec.Tags)
	spec.Tags[0] = "changed again"
	spec.Payload.Schema[0] = '['
	fresh := set.Specs()[0]
	assert.Equal(t, []string{"read"}, fresh.Tags)
	value, err := fresh.Payload.Codec.FromJSON([]byte(`{"query":"records"}`))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"query": "records"}, value)
	require.NoError(t, set.ValidatePayload("lookup.find", value))
	require.Error(t, set.ValidatePayload("lookup.find", rawjson.Message(`{}`)))
	require.Error(t, set.ValidatePayload("lookup.missing", value))
	require.NoError(t, set.ValidateResult("lookup.find", 1))
	require.Error(t, set.ValidateResult("lookup.find", "one"))
	require.Error(t, set.ValidateResult("lookup.missing", 1))

	_, err = fresh.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"records"}`))
	require.Error(t, err)
	_, err = fresh.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"records","cursor":"page"}`))
	require.NoError(t, err)
	meta, ok := set.MetadataByName("lookup.find")
	require.True(t, ok)
	meta.Tags[0] = "changed"
	meta, ok = set.MetadataByName("lookup.find")
	require.True(t, ok)
	assert.Equal(t, []string{"read"}, meta.Tags)
	_, ok = set.MetadataByName("lookup.missing")
	assert.False(t, ok)
}

func TestDiscoveredToolsetRejectsIncompleteContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*ToolsetSchema)
		want   string
	}{
		{
			name: "no tools",
			change: func(s *ToolsetSchema) {
				s.Tools = nil
			},
			want: "has no tools",
		},
		{
			name: "nil tool",
			change: func(s *ToolsetSchema) {
				s.Tools[0] = nil
			},
			want: "nil tool",
		},
		{
			name: "foreign name",
			change: func(s *ToolsetSchema) {
				s.Tools[0].Name = "other.find"
			},
			want: "does not belong",
		},
		{
			name: "missing method",
			change: func(s *ToolsetSchema) {
				s.Tools[0].Name = "lookup."
			},
			want: "does not belong",
		},
		{
			name: "duplicate",
			change: func(s *ToolsetSchema) {
				s.Tools = append(s.Tools, s.Tools[0])
			},
			want: "repeats tool",
		},
		{
			name: "no model schema",
			change: func(s *ToolsetSchema) {
				s.Tools[0].PayloadSchema = nil
			},
			want: "payload: schema is required",
		},
		{
			name: "no execution schema",
			change: func(s *ToolsetSchema) {
				s.Tools[0].ExecutionPayloadSchema = nil
			},
			want: "execution payload: schema is required",
		},
		{
			name: "no result schema",
			change: func(s *ToolsetSchema) {
				s.Tools[0].ResultSchema = nil
			},
			want: "result: schema is required",
		},
		{
			name: "server-only data",
			change: func(s *ToolsetSchema) {
				s.Tools[0].SidecarSchema = rawjson.Message(`{}`)
			},
			want: "requires a static contract",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := discoveredSchema()
			test.change(source)
			_, err := NewToolset(source)
			require.ErrorContains(t, err, test.want)
		})
	}
	_, err := NewToolset(nil)
	require.Error(t, err)
}

func TestDiscoveredToolNamesHaveStableOrder(t *testing.T) {
	t.Parallel()

	source := discoveredSchema()
	second := *source.Tools[0]
	second.Name = "lookup.before"
	source.Tools = append(source.Tools, &second)
	set, err := NewToolset(source)
	require.NoError(t, err)
	assert.Equal(t, []tools.Ident{"lookup.before", "lookup.find"}, set.Names())
	for _, name := range set.Names() {
		spec, ok := set.Spec(name)
		require.True(t, ok)
		assert.Equal(t, name, spec.Name)
	}
}

func discoveredSchema() *ToolsetSchema {
	return &ToolsetSchema{
		Name:    "lookup",
		Version: "1.2.3",
		Tools: []*ToolSchema{{
			Name:                   "lookup.find",
			Description:            "Find matching records.",
			Tags:                   []string{"read"},
			PayloadSchema:          rawjson.Message(`{"type":"object","required":["query"],"additionalProperties":false,"properties":{"query":{"type":"string"}}}`),
			ExecutionPayloadSchema: rawjson.Message(`{"type":"object","required":["query","cursor"],"additionalProperties":false,"properties":{"query":{"type":"string"},"cursor":{"type":"string"}}}`),
			ResultSchema:           rawjson.Message(`{"type":"integer","minimum":1}`),
		}},
	}
}
