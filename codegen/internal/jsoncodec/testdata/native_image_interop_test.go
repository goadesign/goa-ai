// These assertions use actual generated entry points. No transport files,
// validators, service providers, or transforms are removed from the fixture.
package sample_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	sample "codec.local/gen/sample"
	pictures "codec.local/gen/sample/toolsets/pictures"
	picturehttp "codec.local/gen/sample/toolsets/pictures/http"
	types "codec.local/gen/types"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	registrycontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

const originalImage = `{"sourceDocumentId":"12345678-1234-4234-8234-123456789abc","contentSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","format":"png","sizeBytes":1}`
const modelImage = `{"source_document_id":"12345678-1234-4234-8234-123456789abc","content_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","format":"png","size_bytes":1}`
const originalGraph = `{"firstNode":{"displayName":"first","ownerId":"12345678-1234-4234-8234-123456789abc","nextNode":{"displayName":"next"},"choiceData":{"type":"detail","value":{"detailText":"selected"}}},"secondNode":{"displayName":"second"},"nodeList":[{"displayName":"list"}],"nodeMap":{"selected":{"displayName":"map"}},"aliasNode":[{"displayName":"alias"}],"collisionNode":{"collisionValue":"ordinary"}}`
const modelGraph = `{"first_node":{"display_name":"first","owner_id":"12345678-1234-4234-8234-123456789abc","next_node":{"display_name":"next"},"choice_data":{"type":"detail","value":{"detail_text":"selected"}}},"second_node":{"display_name":"second"},"node_list":[{"display_name":"list"}],"node_map":{"selected":{"display_name":"map"}},"alias_node":[{"display_name":"alias"}],"collision_node":{"collision_value":"ordinary"}}`

func TestNativeImageTypedProducerAndSharedCodec(t *testing.T) {
	want := &types.ImageSource{
		SourceDocumentID: "12345678-1234-4234-8234-123456789abc",
		ContentSHA256:    strings.Repeat("a", 64), Format: "png", SizeBytes: 1,
	}
	producer := &pictures.ViewImageFixtureImageServerData{
		SourceDocumentID: want.SourceDocumentID,
		ContentSHA256:    want.ContentSHA256, Format: want.Format, SizeBytes: want.SizeBytes,
	}
	typed := pictures.ViewImageFixtureImageServerDataCodec()
	data, err := typed.ToJSON(producer)
	require.NoError(t, err)
	require.JSONEq(t, originalImage, string(data))
	got, err := types.DecodeImageSource(data)
	require.NoError(t, err)
	require.Equal(t, want, got)

	data, err = types.EncodeImageSource(want)
	require.NoError(t, err)
	restored, err := typed.FromJSON(data)
	require.NoError(t, err)
	require.Equal(t, producer, restored)
	data, err = typed.ToJSON(restored)
	require.NoError(t, err)
	require.JSONEq(t, originalImage, string(data))

	spec := pictures.SpecViewImage()
	declaration := serverData(t, spec, "fixture.image")
	for _, codec := range []tools.JSONCodec[any]{
		declaration.Type.Codec,
		pictures.NativeImageSources()[spec.Name][0].Type.Codec,
	} {
		value, err := codec.FromJSON(data)
		require.NoError(t, err)
		encoded, err := codec.ToJSON(value)
		require.NoError(t, err)
		decoded, err := types.DecodeImageSource(encoded)
		require.NoError(t, err)
		require.Equal(t, want, decoded)
		canonical := canonicalItem(t, spec, "fixture.image", encoded)
		decoded, err = types.DecodeImageSource(canonical)
		require.NoError(t, err)
		require.Equal(t, want, decoded)
	}
	canonical := canonicalItem(t, spec, "fixture.image", data)
	require.Equal(t, canonical, canonicalItem(t, spec, "fixture.image", canonical))
	validateTypeSpec(t, declaration.Type, originalImage)
}

// The method returns the shared public type. The generated provider must
// transform and encode it using the marked tool's transport contract.
type imageService struct {
	output *sample.ReadOutput
}

func (service imageService) Read(_ context.Context, input *sample.ReadInput) (*sample.ReadOutput, error) {
	if input.RequestKey != "selected" {
		panic("unexpected generated method input")
	}
	return service.output, nil
}

func TestNativeImageGeneratedMethodProducer(t *testing.T) {
	image, err := types.DecodeImageSource([]byte(originalImage))
	require.NoError(t, err)
	provider := pictures.NewProvider(imageService{output: &sample.ReadOutput{Ready: true, Image: image}})
	spec := pictures.SpecViewImage()
	ctx := toolregistry.WithToolUseID(context.Background(), "synthetic-call")
	result, err := provider.HandleToolCall(ctx, toolregistry.ToolCallMessage{
		Type:      toolregistry.MessageTypeCall,
		ToolUseID: "synthetic-call", RegistrationToken: strings.Repeat("a", 64),
		Tool: spec.Name, Payload: json.RawMessage(`{"request_key":"selected"}`),
		Meta: &toolregistry.ToolCallMeta{},
	})
	require.NoError(t, err)
	require.Nil(t, result.Error)
	require.JSONEq(t, `{"ready":true}`, string(result.Result))
	require.Len(t, result.ServerData, 1)
	require.Equal(t, "fixture.image", result.ServerData[0].Kind)
	got, err := types.DecodeImageSource(result.ServerData[0].Data)
	require.NoError(t, err)
	require.Equal(t, image, got)
	canonical := canonicalItem(t, spec, "fixture.image", result.ServerData[0].Data)
	got, err = types.DecodeImageSource(canonical)
	require.NoError(t, err)
	require.Equal(t, image, got)
}

func TestNativeImageSchemaMetadataAndManifest(t *testing.T) {
	spec := pictures.SpecViewImage()
	declaration := serverData(t, spec, "fixture.image")
	sources := pictures.NativeImageSources()
	require.Len(t, sources, 2)
	require.Len(t, sources[spec.Name], 1)
	manifest := sources[spec.Name][0]
	require.True(t, manifest.NativeImage)
	require.Equal(t, declaration.Kind, manifest.Kind)
	require.Equal(t, declaration.Audience, manifest.Audience)
	require.Equal(t, declaration.Description, manifest.Description)
	require.Equal(t, declaration.Type.Name, manifest.Type.Name)
	require.Equal(t, declaration.Type.Schema, manifest.Type.Schema)
	require.Equal(t, declaration.Type.SchemaWithoutRootExample, manifest.Type.SchemaWithoutRootExample)
	require.Equal(t, declaration.Type.ExampleJSON, manifest.Type.ExampleJSON)
	require.Equal(t, declaration.Type.Fields, manifest.Type.Fields)
	require.Empty(t, declaration.Type.ExampleJSON, "server-data root example policy stays unchanged")

	for _, raw := range []tools.RawJSON{manifest.Type.Schema, manifest.Type.SchemaWithoutRootExample} {
		var schema map[string]any
		require.NoError(t, json.Unmarshal(raw, &schema))
		properties := schema["properties"].(map[string]any)
		require.ElementsMatch(t, []string{"sourceDocumentId", "contentSHA256", "format", "sizeBytes"}, mapKeys(properties))
		require.ElementsMatch(t, []any{"sourceDocumentId", "contentSHA256", "format", "sizeBytes"}, schema["required"])
		require.Equal(t, false, schema["additionalProperties"])
		require.Contains(t, string(raw), "Document that owns the image.")
	}
	for _, path := range []string{"sourceDocumentId", "contentSHA256", "format", "sizeBytes"} {
		_, found := tools.LookupFieldMetadata(manifest.Type.Fields, path)
		require.True(t, found, path)
	}
	registryCount := 0
	for _, schema := range pictures.ToolSchemas() {
		if schema.Name != string(spec.Name) {
			continue
		}
		compiled, err := registrycontract.Compile(schema)
		require.NoError(t, err)
		compiledImage := serverData(t, compiled, "fixture.image")
		// Empty branch lists mean the field is outside a union. Compare copied
		// metadata so nil and allocated empty lists have the same representation.
		require.Equal(t, tools.CloneFieldMetadata(manifest.Type.Fields), tools.CloneFieldMetadata(compiledImage.Type.Fields))
		require.Equal(t, manifest.Type.Name, compiledImage.Type.Name)
		require.True(t, compiledImage.NativeImage)
		value, err := compiledImage.Type.Codec.FromJSON([]byte(originalImage))
		require.NoError(t, err)
		encoded, err := compiledImage.Type.Codec.ToJSON(value)
		require.NoError(t, err)
		decoded, err := types.DecodeImageSource(encoded)
		require.NoError(t, err)
		require.EqualValues(t, 1, decoded.SizeBytes)
		for _, item := range schema.ConsumerContract.ServerData {
			if item.Kind != "fixture.image" {
				continue
			}
			registryCount++
			require.True(t, item.NativeImage)
			require.Equal(t, []byte(manifest.Type.Schema), item.Schema)
			require.Equal(t, []byte(manifest.Type.SchemaWithoutRootExample), item.Type.SchemaWithoutRootExample)
			require.Equal(t, []byte(manifest.Type.ExampleJSON), item.Type.ExampleJSON)
			require.Len(t, item.Type.Fields, len(manifest.Type.Fields))
		}
	}
	require.Equal(t, 1, registryCount)
	artifact, err := os.ReadFile("agents/reader/specs/tool_schemas.json")
	require.NoError(t, err)
	var document struct {
		Tools []struct {
			ID         string `json:"id"`
			ServerData []struct {
				Kind        string `json:"kind"`
				NativeImage bool   `json:"native_image"`
				Type        struct {
					Schema json.RawMessage `json:"schema"`
				} `json:"type"`
			} `json:"server_data"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(artifact, &document))
	artifactCount := 0
	for _, tool := range document.Tools {
		if tool.ID == string(spec.Name) {
			for _, item := range tool.ServerData {
				if item.Kind == "fixture.image" {
					artifactCount++
					require.True(t, item.NativeImage)
					require.JSONEq(t, string(manifest.Type.Schema), string(item.Type.Schema))
				}
			}
		}
	}
	require.Equal(t, 1, artifactCount)
	manifest.Type.Schema[0] = '!'
	field, found := tools.LookupFieldMetadata(manifest.Type.Fields, "sourceDocumentId")
	require.True(t, found)
	field.Path[0] = tools.FixedField("changed")
	delete(sources, spec.Name)
	fresh := pictures.NativeImageSources()[spec.Name][0]
	require.Equal(t, declaration.Type.Schema, fresh.Type.Schema)
	require.Equal(t, declaration.Type.Fields, fresh.Type.Fields)
}

func TestNativeImageRejectsOtherJSONContracts(t *testing.T) {
	spec := pictures.SpecViewImage()
	generic := serverData(t, spec, "fixture.image").Type.Codec
	reject := func(t *testing.T, data []byte) {
		t.Helper()
		_, err := types.DecodeImageSource(data)
		require.Error(t, err)
		_, err = pictures.ViewImageFixtureImageServerDataCodec().FromJSON(data)
		require.Error(t, err)
		_, err = generic.FromJSON(data)
		require.Error(t, err)
		envelope, err := toolregistry.EncodeServerData([]*toolregistry.ServerDataItem{
			{Kind: "fixture.image", Audience: "evidence", Data: data},
		})
		require.NoError(t, err)
		_, err = spec.CanonicalizeServerData(tools.RawJSON(envelope))
		require.Error(t, err)
	}
	for _, field := range []string{"sourceDocumentId", "contentSHA256", "format", "sizeBytes"} {
		for _, mode := range []string{"missing", "null", "wrong type"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				var value map[string]any
				require.NoError(t, json.Unmarshal([]byte(originalImage), &value))
				switch mode {
				case "missing":
					delete(value, field)
				case "null":
					value[field] = nil
				case "wrong type":
					value[field] = false
				}
				data, err := json.Marshal(value)
				require.NoError(t, err)
				reject(t, data)
			})
		}
	}
	for _, invalid := range []string{
		modelImage,
		strings.Replace(originalImage, `"sizeBytes":1`, `"size_bytes":1`, 1),
		strings.Replace(originalImage, `"sizeBytes":1`, `"sizeBytes":1,"size_bytes":1`, 1),
		strings.Replace(originalImage, `"sizeBytes":1`, `"sizeBytes":1,"extra":true`, 1),
		strings.Replace(originalImage, "12345678-1234-4234-8234-123456789abc", "not-an-id", 1),
		strings.Replace(originalImage, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		strings.Replace(originalImage, `"png"`, `"bmp"`, 1),
		strings.Replace(originalImage, `"sizeBytes":1`, `"sizeBytes":0`, 1),
	} {
		reject(t, []byte(invalid))
	}
	for _, item := range []*toolregistry.ServerDataItem{
		{Kind: "fixture.unknown", Audience: "evidence", Data: []byte(originalImage)},
		{Kind: "fixture.image", Audience: "timeline", Data: []byte(originalImage)},
	} {
		envelope, err := toolregistry.EncodeServerData([]*toolregistry.ServerDataItem{item})
		require.NoError(t, err)
		_, err = spec.CanonicalizeServerData(tools.RawJSON(envelope))
		require.Error(t, err)
	}
}

func TestNativeImageRecursiveGraphAndOrdinaryPreservation(t *testing.T) {
	spec := pictures.SpecNativeGraph()
	native := serverData(t, spec, "fixture.graph")
	for _, marked := range pictures.NativeImageSources()[spec.Name] {
		declared := serverData(t, spec, marked.Kind)
		require.Equal(t, declared.Type.Schema, marked.Type.Schema)
		require.Equal(t, declared.Type.SchemaWithoutRootExample, marked.Type.SchemaWithoutRootExample)
		require.Equal(t, declared.Type.Fields, marked.Type.Fields)
	}
	shared, err := types.DecodeGraph([]byte(originalGraph))
	require.NoError(t, err)
	data, err := types.EncodeGraph(shared)
	require.NoError(t, err)
	generated, err := pictures.NativeGraphFixtureGraphServerDataCodec().FromJSON(data)
	require.NoError(t, err)
	data, err = pictures.NativeGraphFixtureGraphServerDataCodec().ToJSON(generated)
	require.NoError(t, err)
	require.JSONEq(t, originalGraph, string(data))
	restored, err := types.DecodeGraph(data)
	require.NoError(t, err)
	require.Equal(t, shared, restored)
	data = canonicalItem(t, spec, "fixture.graph", data)
	restored, err = types.DecodeGraph(data)
	require.NoError(t, err)
	require.Equal(t, shared, restored)
	validateTypeSpec(t, native.Type, originalGraph)
	require.Contains(t, string(native.Type.Schema), `"displayName":"leaf"`)
	require.Contains(t, string(native.Type.SchemaWithoutRootExample), `"displayName":"leaf"`)
	require.NotContains(t, string(native.Type.Schema), `"display_name"`)
	require.NotContains(t, string(native.Type.SchemaWithoutRootExample), `"display_name"`)
	for _, field := range native.Type.Fields {
		for _, segment := range field.Path {
			if name, fixed := segment.(tools.FixedField); fixed {
				require.NotContains(t, string(name), "_")
			}
		}
	}
	for _, path := range []string{
		"firstNode.displayName", "secondNode.displayName", "nodeList.0.displayName",
		"nodeMap.selected.displayName", "aliasNode.0.displayName",
		"firstNode.choiceData.value.detailText", "collisionNode.collisionValue",
	} {
		_, found := tools.LookupFieldMetadata(native.Type.Fields, path)
		require.True(t, found, path)
	}
	inline := serverData(t, spec, "fixture.inline")
	inlineJSON := `{"displayName":"inline","childNode":{"displayName":"child"}}`
	validateTypeSpec(t, inline.Type, inlineJSON)
	inlineValue, err := inline.Type.Codec.FromJSON([]byte(inlineJSON))
	require.NoError(t, err)
	data, err = inline.Type.Codec.ToJSON(inlineValue)
	require.NoError(t, err)
	require.JSONEq(t, inlineJSON, string(canonicalItem(t, spec, inline.Kind, data)))

	ordinary := pictures.SpecOrdinary()
	roundTripCodec(t, pictures.OrdinaryPayloadCodec(), modelImage)
	roundTripCodec(t, pictures.OrdinaryResultCodec(), modelImage)
	roundTripCodec(t, pictures.OrdinaryFixtureUnmarkedServerDataCodec(), modelImage)
	for _, valueType := range []tools.TypeSpec{ordinary.Payload, ordinary.Result, serverData(t, ordinary, "fixture.unmarked").Type} {
		value, err := valueType.Codec.FromJSON([]byte(modelImage))
		require.NoError(t, err)
		data, err := valueType.Codec.ToJSON(value)
		require.NoError(t, err)
		require.JSONEq(t, modelImage, string(data))
		validateTypeSpec(t, valueType, modelImage)
		_, err = valueType.Codec.FromJSON([]byte(originalImage))
		require.Error(t, err)
	}
	require.JSONEq(t, modelImage, string(canonicalItem(t, ordinary, "fixture.unmarked", []byte(modelImage))))
	citation := serverData(t, ordinary, "fixture.citation")
	citationJSON := `{"file_id":"file-a","details":{"detail_text":"selected"}}`
	validateTypeSpec(t, citation.Type, citationJSON)
	require.JSONEq(t, citationJSON, string(canonicalItem(t, ordinary, citation.Kind, []byte(citationJSON))))
	graph := pictures.SpecGraph()
	for _, valueType := range []tools.TypeSpec{graph.Payload, graph.Result, serverData(t, graph, "fixture.graph.ordinary").Type} {
		value, err := valueType.Codec.FromJSON([]byte(modelGraph))
		require.NoError(t, err)
		data, err := valueType.Codec.ToJSON(value)
		require.NoError(t, err)
		require.JSONEq(t, modelGraph, string(data))
		validateTypeSpec(t, valueType, modelGraph)
		require.NotContains(t, string(valueType.Schema), `"displayName"`)
		require.Contains(t, string(valueType.Schema), `"display_name":"leaf"`)
		_, err = valueType.Codec.FromJSON([]byte(originalGraph))
		require.Error(t, err)
	}
	modelTransport := reflect.TypeOf(picturehttp.GraphPayloadTransport{})
	nativeTransport := reflect.TypeOf(picturehttp.NativeGraphFixtureGraphServerDataTransport{})
	modelFirst, ok := modelTransport.FieldByName("FirstNode")
	require.True(t, ok)
	nativeFirst, ok := nativeTransport.FieldByName("FirstNode")
	require.True(t, ok)
	nativeSecond, ok := nativeTransport.FieldByName("SecondNode")
	require.True(t, ok)
	require.Equal(t, nativeFirst.Type, nativeSecond.Type, "repeated native nodes share one declaration")
	require.NotEqual(t, modelFirst.Type, nativeFirst.Type, "ordinary nodes use a different transport")
	ordinaryCollision, ok := modelTransport.FieldByName("CollisionNode")
	require.True(t, ok)
	require.Equal(t, "NodeNativeImageTransport", ordinaryCollision.Type.Elem().Name())
	require.NotEqual(t, ordinaryCollision.Type, nativeFirst.Type, "native Node cannot steal an ordinary type's name")
}

func serverData(t *testing.T, spec tools.ToolSpec, kind string) tools.ServerDataSpec {
	t.Helper()
	for _, item := range spec.ServerData {
		if item.Kind == kind {
			return *item
		}
	}
	t.Fatalf("generated tool %q lacks kind %q", spec.Name, kind)
	return tools.ServerDataSpec{}
}

func canonicalItem(t *testing.T, spec tools.ToolSpec, kind string, data []byte) []byte {
	t.Helper()
	envelope, err := toolregistry.EncodeServerData([]*toolregistry.ServerDataItem{
		{Kind: kind, Audience: "evidence", Data: data},
	})
	require.NoError(t, err)
	canonical, err := spec.CanonicalizeServerData(tools.RawJSON(envelope))
	require.NoError(t, err)
	items, err := toolregistry.DecodeServerData(canonical)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, kind, items[0].Kind)
	require.Equal(t, "evidence", items[0].Audience)
	return items[0].Data
}

// Pin the public typed codec entry points as well as their generic spec forms.
func roundTripCodec[T any](t *testing.T, codec tools.JSONCodec[T], input string) {
	t.Helper()
	value, err := codec.FromJSON([]byte(input))
	require.NoError(t, err)
	data, err := codec.ToJSON(value)
	require.NoError(t, err)
	require.JSONEq(t, input, string(data))
}

// Validate both schema variants and every emitted nested authored example using
// the existing JSON Schema implementation, including definitions and unions.
func validateTypeSpec(t *testing.T, spec tools.TypeSpec, sample string) {
	t.Helper()
	for _, raw := range []tools.RawJSON{spec.Schema, spec.SchemaWithoutRootExample} {
		var document any
		require.NoError(t, json.Unmarshal(raw, &document))
		compiler := jsonschema.NewCompiler()
		require.NoError(t, compiler.AddResource("schema.json", document))
		schema, err := compiler.Compile("schema.json")
		require.NoError(t, err)
		var value any
		require.NoError(t, json.Unmarshal([]byte(sample), &value))
		require.NoError(t, schema.Validate(value))
		var visit func(any, string)
		visit = func(node any, pointer string) {
			switch node := node.(type) {
			case map[string]any:
				if example, ok := node["example"]; ok {
					child, err := compiler.Compile("schema.json#" + pointer)
					require.NoError(t, err)
					require.NoError(t, child.Validate(example), pointer)
				}
				for key, child := range node {
					if key == "example" || key == "examples" {
						continue
					}
					escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
					visit(child, pointer+"/"+escaped)
				}
			case []any:
				for i, child := range node {
					visit(child, pointer+"/"+strconv.Itoa(i))
				}
			}
		}
		visit(document, "")
		if len(spec.ExampleJSON) > 0 {
			require.NoError(t, json.Unmarshal(spec.ExampleJSON, &value))
			require.NoError(t, schema.Validate(value))
		}
	}
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}
