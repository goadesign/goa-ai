// Current tool declarations choose which result items become images. Retained
// kind declarations decode saved messages independently of executable tools.
// These tests exercise both contracts without reading image bytes.
package runtime

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	genshared "goa.design/goa-ai/internal/testimageshared/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolserverdata"
)

func TestNativeImageCurrentItemMarkerControlsProduction(t *testing.T) {
	manifest := genpictures.NativeImageSources()
	historical := manifest[genpictures.View][0]
	current := historical
	current.Kind = "fixture.current.v1"
	manifest[genpictures.View] = append(manifest[genpictures.View], current)
	rt := New(inmem.New(), WithImageSourceResolver(manifest, noContractImageRead))
	spec := genpictures.SpecView()
	spec.ServerData[0].NativeImage = false
	spec.ServerData = append(spec.ServerData, &current)
	// The envelope admits two current evidence items through the generated
	// descriptor codec. Only the selected tool's marker distinguishes images.
	spec.CanonicalizeServerData = func(data tools.RawJSON) (tools.RawJSON, error) {
		return toolserverdata.Canonicalize(data, func(kind, audience string, data rawjson.Message) (string, rawjson.Message, error) {
			require.Contains(t, []string{historical.Kind, current.Kind}, kind)
			require.Equal(t, "evidence", audience)
			value, err := historical.Type.Codec.FromJSON(data)
			if err != nil {
				return "", nil, err
			}
			encoded, err := historical.Type.Codec.ToJSON(value)
			return audience, encoded, err
		})
	}
	oldSource := nativeImageSource(t, "old-selection", 73)
	newSource := nativeImageSource(t, "new-selection", 73)
	envelope := rawjson.Message(`[{"kind":"fixture.image.v1","audience":"evidence","data":` + string(oldSource.Data) +
		`},{"kind":"fixture.current.v1","audience":"evidence","data":` + string(newSource.Data) + `}]`)
	require.NoError(t, rt.validateImageSourceProducer(spec))
	parts, err := rt.toolImageSources(spec, envelope)
	require.NoError(t, err)
	require.Equal(t, []model.Part{model.ImageSourcePart{SourceKind: current.Kind, Data: newSource.Data}}, parts)

	// An earlier selected tool contract still marks A, even while B is admitted.
	parts, err = rt.toolImageSources(genpictures.SpecView(), rawjson.Message(
		`[{"kind":"fixture.image.v1","audience":"evidence","data":`+string(oldSource.Data)+`}]`))
	require.NoError(t, err)
	require.Equal(t, []model.Part{oldSource}, parts)
	spec.ServerData[1].NativeImage = false
	parts, err = rt.toolImageSources(spec, envelope)
	require.NoError(t, err)
	assert.Empty(t, parts)

	// No executable tool has been registered, but old canonical messages remain
	// decodable. Retaining that codec must not mark new ordinary evidence.
	require.NoError(t, rt.validateHistoricalImageSources([]*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{oldSource},
	}}))
	spec.ServerData[1].NativeImage = true
	spec.ServerData[1].Kind = "unadmitted.v1"
	_, err = rt.toolImageSources(spec, envelope)
	require.ErrorContains(t, err, "does not match its admitted contract")
}

func TestNativeImageSharedGeneratedKindContract(t *testing.T) {
	manifest := genshared.NativeImageSources()
	view, inspect := manifest[genshared.View][0], manifest[genshared.Inspect][0]
	require.NotEqual(t, view.Type.Name, inspect.Type.Name)
	left, right := view, inspect
	left.Type = cloneTypeSpec(left.Type)
	right.Type = cloneTypeSpec(right.Type)
	left.Type.Name, right.Type.Name = "", ""
	left.Type.Codec, right.Type.Codec = tools.JSONCodec[any]{}, tools.JSONCodec[any]{}
	require.Equal(t, left, right, "only the producer-specific top-level name and codec Go types may differ")
	t.Logf("generated producer aliases: %s / %s", view.Type.Name, inspect.Type.Name)
	for _, data := range []rawjson.Message{
		rawjson.Message(`{"id":"retained"}`),
		rawjson.Message(`{"id":""}`),
		rawjson.Message(`{"id":1}`),
		rawjson.Message(`{"id":"retained","unknown":true}`),
		rawjson.Message(`null`),
	} {
		viewValue, viewErr := view.Type.Codec.FromJSON(data)
		inspectValue, inspectErr := inspect.Type.Codec.FromJSON(data)
		require.Equal(t, viewErr == nil, inspectErr == nil)
		if viewErr != nil {
			continue
		}
		viewJSON, err := view.Type.Codec.ToJSON(viewValue)
		require.NoError(t, err)
		inspectJSON, err := inspect.Type.Codec.ToJSON(inspectValue)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(viewJSON, inspectJSON), "producer codecs emit identical descriptor bytes")
	}

	var decodedBy []tools.Ident
	for producer, declarations := range manifest {
		decoder := declarations[0].Type.Codec.FromJSON
		declarations[0].Type.Codec.FromJSON = func(data []byte) (any, error) {
			decodedBy = append(decodedBy, producer)
			return decoder(data)
		}
	}
	var rt *Runtime
	require.NotPanics(t, func() {
		rt = New(inmem.New(), WithImageSourceResolver(manifest, noContractImageRead))
	})
	require.NoError(t, rt.validateImageSourceProducer(genshared.SpecView()))
	require.NoError(t, rt.validateImageSourceProducer(genshared.SpecInspect()))
	data := rawjson.Message(`{"id":"retained"}`)
	require.NoError(t, rt.validateHistoricalImageSources([]*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ImageSourcePart{SourceKind: view.Kind, Data: data},
		},
	}}))
	assert.Equal(t, []tools.Ident{genshared.Inspect}, decodedBy, "sorted producer names retain inspect's generated decoder")

	exact := genshared.SpecView()
	exact.ServerData[0].Type.Name = inspect.Type.Name
	require.ErrorContains(t, rt.validateImageSourceProducer(exact), "does not match its admitted contract")
	for _, tc := range []struct {
		name   string
		change func(*tools.ServerDataSpec)
	}{
		{"different generated schema", func(spec *tools.ServerDataSpec) {
			spec.Type = genpictures.NativeImageSources()[genpictures.View][0].Type
		}},
		{"field metadata", func(spec *tools.ServerDataSpec) { spec.Type.Fields[0].Description = "Different contract." }},
		{"source description", func(spec *tools.ServerDataSpec) { spec.Description = "Different contract." }},
		{"audience", func(spec *tools.ServerDataSpec) { spec.Audience = "ui" }},
		{"unmarked", func(spec *tools.ServerDataSpec) { spec.NativeImage = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			incompatible := genshared.NativeImageSources()
			tc.change(&incompatible[genshared.View][0])
			assert.Panics(t, func() {
				New(inmem.New(), WithImageSourceResolver(incompatible, noContractImageRead))
			})
		})
	}
}

func noContractImageRead(context.Context, run.Context, model.ImageSourcePart, int64) (model.ImagePart, error) {
	panic("declaration and historical decoding must not read images")
}
