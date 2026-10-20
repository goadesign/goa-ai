// These checks compare independently constructed tool declarations and verify
// that equality checks preserve their codecs, metadata and empty-value rules.
package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestEquivalentToolSpecPreservesNestedCodecs(t *testing.T) {
	spec := newAnyJSONSpec("sample.inspect")
	spec.Result = spec.Payload
	spec.TextOnly = &tools.ModelContract{Payload: spec.Payload, ExecutionCodec: tools.AnyJSONCodec}
	spec.ServerData = []*tools.ServerDataSpec{{Kind: "sample", Type: spec.Payload}, nil}
	other := cloneToolSpec(spec)
	other.TextOnly.Payload.Codec.FromJSON = func([]byte) (any, error) { return "other", nil }
	other.ServerData[0].Type.Codec.FromJSON = other.TextOnly.Payload.Codec.FromJSON

	assert.True(t, equivalentToolSpec(spec, other), "codec implementations do not change declaration equality")
	for _, codec := range []tools.JSONCodec[any]{
		spec.Payload.Codec, spec.Result.Codec, spec.ExecutionPayloadCodec,
		spec.TextOnly.Payload.Codec, spec.TextOnly.ExecutionCodec, spec.ServerData[0].Type.Codec,
	} {
		require.NotNil(t, codec.FromJSON)
		decoded, err := codec.FromJSON([]byte(`{"value":"original"}`))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"value": "original"}, decoded)
	}
	decoded, err := other.TextOnly.Payload.Codec.FromJSON(nil)
	require.NoError(t, err)
	assert.Equal(t, "other", decoded)
	decoded, err = other.ServerData[0].Type.Codec.FromJSON(nil)
	require.NoError(t, err)
	assert.Equal(t, "other", decoded)
}

func TestEquivalentToolSpecRetainsNestedDifferences(t *testing.T) {
	spec := newAnyJSONSpec("sample.inspect")
	spec.Meta = map[string][]string{"category": {"lookup"}}
	spec.Payload.Fields = []tools.FieldMetadata{{Description: "Original field"}}
	spec.TextOnly = &tools.ModelContract{Payload: spec.Payload, ExecutionCodec: tools.AnyJSONCodec}
	spec.ServerData = []*tools.ServerDataSpec{{Kind: "sample", Type: spec.Payload}}
	tests := []struct {
		name   string
		change func(*tools.ToolSpec)
	}{
		{"schema", func(s *tools.ToolSpec) { s.Payload.Schema[2] = 'X' }},
		{"field metadata", func(s *tools.ToolSpec) { s.Payload.Fields[0].Description = "Changed field" }},
		{"metadata value", func(s *tools.ToolSpec) { s.Meta["category"][0] = "changed" }},
		{"text-only schema", func(s *tools.ToolSpec) { s.TextOnly.Payload.Schema[2] = 'X' }},
		{"text-only field metadata", func(s *tools.ToolSpec) { s.TextOnly.Payload.Fields[0].Description = "Changed field" }},
		{"server-data schema", func(s *tools.ToolSpec) { s.ServerData[0].Type.Schema[2] = 'X' }},
		{"server-data field metadata", func(s *tools.ToolSpec) { s.ServerData[0].Type.Fields[0].Description = "Changed field" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := cloneToolSpec(spec)
			other := cloneToolSpec(spec)
			test.change(&other)
			assert.False(t, equivalentToolSpec(spec, other))
			assert.Equal(t, before.Payload.Schema, spec.Payload.Schema)
			assert.Equal(t, before.Payload.Fields[0].Description, spec.Payload.Fields[0].Description)
			assert.Equal(t, before.Meta, spec.Meta)
			assert.Equal(t, before.TextOnly.Payload.Schema, spec.TextOnly.Payload.Schema)
			assert.Equal(t, before.TextOnly.Payload.Fields[0].Description, spec.TextOnly.Payload.Fields[0].Description)
			assert.Equal(t, before.ServerData[0].Type.Schema, spec.ServerData[0].Type.Schema)
			assert.Equal(t, before.ServerData[0].Type.Fields[0].Description, spec.ServerData[0].Type.Fields[0].Description)
		})
	}
}

func TestEquivalentToolSpecPreservesEmptyMetadataRules(t *testing.T) {
	base := tools.ToolSpec{
		Meta:       map[string][]string{"empty": nil},
		Payload:    tools.TypeSpec{Fields: []tools.FieldMetadata{{}}},
		TextOnly:   &tools.ModelContract{},
		ServerData: []*tools.ServerDataSpec{{}},
	}
	empty := cloneToolSpec(base)
	empty.Tags = []string{}
	empty.Meta["empty"] = []string{}
	empty.ExecutionPayloadSchema = tools.RawJSON{}
	empty.Payload.Schema = tools.RawJSON{}
	empty.Payload.SchemaWithoutRootExample = tools.RawJSON{}
	empty.Payload.ExampleJSON = tools.RawJSON{}
	empty.Payload.Fields[0].Path = []tools.FieldPathSegment{}
	empty.Payload.Fields[0].DiscriminatorValues = []string{}
	empty.TextOnly.Payload.Schema = tools.RawJSON{}
	empty.ServerData[0].Type.Schema = tools.RawJSON{}
	assert.True(t, equivalentToolSpec(base, empty))
	assert.Nil(t, base.Payload.Schema)
	assert.NotNil(t, empty.Payload.Schema)
	assert.Nil(t, base.Payload.Fields[0].Path)
	assert.NotNil(t, empty.Payload.Fields[0].Path)
	assert.True(t, equivalentToolSpec(tools.ToolSpec{}, tools.ToolSpec{ServerData: []*tools.ServerDataSpec{}}))

	empty.TextOnly.ExecutionSchema = tools.RawJSON{}
	assert.False(t, equivalentToolSpec(base, empty), "the text-only execution schema retains its existing presence distinction")
	assert.False(t, equivalentToolSpec(tools.ToolSpec{}, tools.ToolSpec{Meta: map[string][]string{}}), "metadata map presence remains significant")
}

func BenchmarkEquivalentToolSpecLargeSchemas(b *testing.B) {
	spec := newAnyJSONSpec("sample.inspect")
	spec.Payload.Schema = tools.RawJSON(`{"type":"object","description":"` + strings.Repeat("x", 256*1024) + `"}`)
	spec.Result = spec.Payload
	other := cloneToolSpec(spec)
	b.ReportAllocs()
	b.SetBytes(int64(len(spec.Payload.Schema) + len(spec.Result.Schema)))
	b.ResetTimer()
	for b.Loop() {
		if !equivalentToolSpec(spec, other) {
			b.Fatal("equal contracts differ")
		}
	}
}
