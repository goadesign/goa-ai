// Generated source admission is independent of executable tool registration.
// These tests exercise construction, production, portable registry contracts,
// and historical decoding without allowing those operations to read images.
package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestNativeImageGeneratedAdmissionAndHistoricalDecoding(t *testing.T) {
	reads := 0
	reader := func(context.Context, run.Context, model.ImageSourcePart, int64) (model.ImagePart, error) {
		reads++
		return model.ImagePart{}, errors.New("no image I/O authorized in this test")
	}
	manifest := genpictures.NativeImageSources()
	rt := New(inmem.New(), WithImageSourceResolver(manifest, reader))
	spec := genpictures.SpecView()
	require.NoError(t, rt.validateImageSourceProducer(spec))
	// Generated registry contracts have schema-backed codecs, not the host's Go
	// function closures. Declarative equivalence admits them without comparing
	// function pointers or requiring JSON object keys in one encoder's order.
	declarations := genpictures.ToolSchemas()
	require.Len(t, declarations, 1)
	portable, err := contract.Compile(declarations[0])
	require.NoError(t, err)
	require.True(t, portable.ServerData[0].NativeImage)
	wantShape, gotShape := *spec.ServerData[0], *portable.ServerData[0]
	wantShape.Type = cloneTypeSpec(wantShape.Type)
	gotShape.Type = cloneTypeSpec(gotShape.Type)
	wantShape.Type.Codec = tools.JSONCodec[any]{}
	gotShape.Type.Codec = tools.JSONCodec[any]{}
	require.Equal(t, wantShape, gotShape)
	require.NoError(t, rt.validateImageSourceProducer(portable))

	source := nativeImageSource(t, "chosen", 73)
	require.NoError(t, rt.validateHistoricalImageSources([]*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{source},
	}}))
	serverData := rawjson.Message(`[{"kind":"fixture.image.v1","audience":"evidence","data":` + string(source.Data) + `}]`)
	parts, err := rt.toolImageSources(portable, serverData)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	recorded := parts[0].(model.ImageSourcePart)
	require.NoError(t, rt.validateHistoricalImageSources([]*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{recorded},
	}}))

	// Mutating caller-owned declarations after construction cannot change the
	// historical decoder or admit a newly marked producer.
	manifest[genpictures.View][0].Type.Schema[0] = '!'
	manifest[genpictures.View][0].Kind = "replacement"
	require.NoError(t, rt.validateImageSourceProducer(genpictures.SpecView()))
	spec.Name = "foreign.view"
	require.ErrorContains(t, rt.validateImageSourceProducer(spec), "does not match")
	unconfigured := New(inmem.New())
	require.ErrorContains(t, unconfigured.validateImageSourceProducer(genpictures.SpecView()), "not admitted")

	for _, tc := range []struct {
		name   string
		source model.ImageSourcePart
	}{
		{"foreign kind", model.ImageSourcePart{SourceKind: "foreign.v1", Data: source.Data}},
		{"unknown field", model.ImageSourcePart{SourceKind: source.SourceKind, Data: rawjson.Message(`{"id":"chosen","format":"png","size":73,"sha256":"` + strings.Repeat("0", 64) + `","secret":true}`)}},
		{"wrong type", model.ImageSourcePart{SourceKind: source.SourceKind, Data: rawjson.Message(`{"id":42}`)}},
		{"null", model.ImageSourcePart{SourceKind: source.SourceKind, Data: rawjson.Message(`null`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, rt.validateHistoricalImageSources([]*model.Message{{
				Role: model.ConversationRoleUser, Parts: []model.Part{tc.source},
			}}))
		})
	}
	_, err = rt.toolImageSources(genpictures.SpecView(), rawjson.Message(`[{"kind":"foreign.v1","audience":"evidence","data":{}}]`))
	require.Error(t, err)
	assert.Zero(t, reads)
}

func TestNativeImageRejectsConflictingDeclarations(t *testing.T) {
	reader := func(context.Context, run.Context, model.ImageSourcePart, int64) (model.ImagePart, error) {
		panic("construction cannot read images")
	}
	for _, tc := range []struct {
		name   string
		change func(map[tools.Ident][]tools.ServerDataSpec)
	}{
		{"different schema", func(m map[tools.Ident][]tools.ServerDataSpec) {
			spec := genpictures.NativeImageSources()[genpictures.View][0]
			spec.Type.Schema = rawjson.Message(`{"type":"string"}`)
			m["other.view"] = []tools.ServerDataSpec{spec}
		}},
		{"wrong audience", func(m map[tools.Ident][]tools.ServerDataSpec) { m[genpictures.View][0].Audience = "ui" }},
		{"unmarked", func(m map[tools.Ident][]tools.ServerDataSpec) { m[genpictures.View][0].NativeImage = false }},
		{"no decoder", func(m map[tools.Ident][]tools.ServerDataSpec) { m[genpictures.View][0].Type.Codec.FromJSON = nil }},
		{"duplicate kind", func(m map[tools.Ident][]tools.ServerDataSpec) {
			m[genpictures.View] = append(m[genpictures.View], m[genpictures.View][0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := genpictures.NativeImageSources()
			tc.change(manifest)
			assert.Panics(t, func() { New(inmem.New(), WithImageSourceResolver(manifest, reader)) })
		})
	}
	manifest := genpictures.NativeImageSources()
	other := genpictures.NativeImageSources()[genpictures.View]
	// Different function values with identical generated declarations are legal.
	fromJSON := other[0].Type.Codec.FromJSON
	other[0].Type.Codec.FromJSON = func(data []byte) (any, error) { return fromJSON(data) }
	manifest["other.view"] = other
	assert.NotPanics(t, func() { New(inmem.New(), WithImageSourceResolver(manifest, reader)) })
}

func nativeImageSource(t *testing.T, id string, size int64) model.ImageSourcePart {
	t.Helper()
	data, err := genpictures.ViewFixtureImageV1ServerDataCodec().ToJSON(&genpictures.ViewFixtureImageV1ServerData{
		ID: id, Format: "png", Size: size, Sha256: strings.Repeat("0", 64),
	})
	require.NoError(t, err)
	return model.ImageSourcePart{SourceKind: "fixture.image.v1", Data: slices.Clone(data)}
}
