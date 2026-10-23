package registrycontract

// These tests restore selected definitions without a registry connection. They
// cover the durable data needed after a provider changes or history is compacted.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

const continuationToolName = "records.continue"

func TestSelectionRetainsOnlyRequiredContracts(t *testing.T) {
	source := testDeclaration()
	target := testDeclaration()
	target.Name = continuationToolName
	target.ConsumerContract.Bounds.Paging.SourceTool = &source.Name
	unrelated := testDeclaration()
	unrelated.Name = "records.unrelated"
	unrelated.ConsumerContract.Bounds = nil
	registration := testResolution(source, target, unrelated)
	catalog, err := Resolve(registration)
	require.NoError(t, err)
	binding, err := catalog.Select("company", tools.Ident(source.Name))
	require.NoError(t, err)
	assert.NotContains(t, string(binding.Resolution), "unrelated")
	assert.Contains(t, string(binding.Resolution), continuationToolName)

	registration.RegistrationToken = strings.Repeat("b", 64)
	source.PayloadSchema = []byte(`{"type":"string"}`)
	restored, err := Read(binding, nil)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 64), restored.Registered.RegistrationToken)
	assert.Len(t, restored.Specs, 2)
	_, err = restored.Specs["records.change"].Payload.Codec.FromJSON([]byte(`{"value":9007199254740993}`))
	require.NoError(t, err)

	next, err := restored.Select("company", continuationToolName)
	require.NoError(t, err)
	continued, err := Read(next, nil)
	require.NoError(t, err)
	assert.Len(t, continued.Specs, 2)
}

func TestResolutionRejectsBrokenPagination(t *testing.T) {
	_, err := Resolve(testResolution(testDeclaration()))
	require.ErrorContains(t, err, "no matching continuation")
	source := testDeclaration()
	target := testDeclaration()
	target.Name = continuationToolName
	wrong := "records.wrong"
	target.ConsumerContract.Bounds.Paging.SourceTool = &wrong
	_, err = Resolve(testResolution(source, target))
	require.Error(t, err)
}

func TestReadRejectsMalformedSavedBindings(t *testing.T) {
	for _, document := range []string{
		`null`,
		`{}`,
		`{"unknown":true}`,
		`{"registrationToken":"bad","toolset":{"name":"records","registeredAt":"2026-09-17T00:00:00Z"}}`,
		`{"toolset":{"tools":[null]}}`,
		`{"toolset":{"tools":[{"consumerContract":{"payload":{"fields":[null]}}}]}}`,
		`{} {}`,
	} {
		t.Run(document, func(t *testing.T) {
			_, err := Read(&tools.RegistryBinding{Registry: "company", Resolution: rawjson.Message(document)}, nil)
			require.Error(t, err)
		})
	}
	_, err := Read(&tools.RegistryBinding{Resolution: rawjson.Message(`{}`)}, nil)
	require.ErrorContains(t, err, "registry is required")
}

func testResolution(declarations ...*genregistry.ToolSchema) *genregistry.ResolvedToolset {
	return &genregistry.ResolvedToolset{
		RegistrationToken: strings.Repeat("a", 64),
		Toolset: &genregistry.Toolset{
			Name: "provider.records", RegisteredAt: "2026-09-17T00:00:00Z", Tools: declarations,
		},
	}
}

func TestResolveRejectsNullExecutableRecords(t *testing.T) {
	for _, test := range []struct {
		name, want string
		mutate     func(*genregistry.ResolvedToolset)
	}{
		{"tool", "registry tool[0]: tool declaration is required", func(r *genregistry.ResolvedToolset) { r.Toolset.Tools[0] = nil }},
		{"contract", "no generated consumer contract", func(r *genregistry.ResolvedToolset) { r.Toolset.Tools[0].ConsumerContract = nil }},
		{"field", "fields[1]", func(r *genregistry.ResolvedToolset) {
			m := r.Toolset.Tools[0].ConsumerContract.Payload
			m.Fields = append(m.Fields, nil)
		}},
		{"path", "path: segment[3]", func(r *genregistry.ResolvedToolset) {
			m := r.Toolset.Tools[0].ConsumerContract.Payload
			m.Fields[0].Path = append(m.Fields[0].Path, nil)
		}},
		{"branch", "branches[1]", func(r *genregistry.ResolvedToolset) {
			m := r.Toolset.Tools[0].ConsumerContract.Payload
			m.Fields[0].Branches = append(m.Fields[0].Branches, nil)
		}},
		{"discriminator", "discriminator: segment[0]", func(r *genregistry.ResolvedToolset) {
			r.Toolset.Tools[0].ConsumerContract.Payload.Fields[0].Branches = []*genregistry.ToolUnionBranch{{
				Selection: genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{Value: "text", Discriminator: []*genregistry.ToolFieldPathSegment{nil}}),
			}}
		}},
		{"untagged path", "branches[0].path: segment[0]", func(r *genregistry.ResolvedToolset) {
			r.Toolset.Tools[0].ConsumerContract.Payload.Fields[0].Branches = []*genregistry.ToolUnionBranch{{
				Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: 0, Path: []*genregistry.ToolFieldPathSegment{nil}}),
			}}
		}},
		{"server data", "server_data[1]", func(r *genregistry.ResolvedToolset) {
			c := r.Toolset.Tools[0].ConsumerContract
			c.ServerData = append(c.ServerData, nil)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			declaration := testDeclaration()
			declaration.ConsumerContract.Bounds = nil
			registered := testResolution(declaration)
			test.mutate(registered)
			require.NoError(t, genregistry.ValidateResolvedToolsetValue(registered))
			_, err := Resolve(registered)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestResolveValidatesOriginalValues(t *testing.T) {
	wrappedNegative := int64(-4294967295)
	for _, test := range []struct {
		name         string
		set          func(*genregistry.ResolvedToolset)
		needsWideInt bool
	}{
		{name: "negative branch index", needsWideInt: true, set: func(r *genregistry.ResolvedToolset) {
			r.Toolset.Tools[0].ConsumerContract.Payload.Fields[0].Branches = []*genregistry.ToolUnionBranch{{
				Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: int(wrappedNegative)}),
			}}
		}},
		{name: "invalid toolset text", set: func(r *genregistry.ResolvedToolset) { r.Toolset.Name = string([]byte{0xff}) }},
		{name: "invalid tool text", set: func(r *genregistry.ResolvedToolset) { r.Toolset.Tools[0].ConsumerContract.Title = string([]byte{0xff}) }},
		{name: "missing required paging list", set: func(r *genregistry.ResolvedToolset) {
			r.Toolset.Tools[0].ConsumerContract.Bounds.Paging.RetainedFields = nil
		}},
		{name: "missing required tools", set: func(r *genregistry.ResolvedToolset) { r.Toolset.Tools = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.needsWideInt && strconv.IntSize < 64 {
				t.Skip("the original Go int cannot represent this value")
			}
			registered := testResolution(testDeclaration())
			test.set(registered)
			_, err := Resolve(registered)
			assert.ErrorContains(t, err, "registry resolution:")
		})
	}
	for _, registered := range []*genregistry.ResolvedToolset{nil, {}, {RegistrationToken: strings.Repeat("a", 64)}} {
		_, err := Resolve(registered)
		assert.Error(t, err)
	}
}

func TestResolvePreservesOriginalValuesAndSavedSelection(t *testing.T) {
	for _, value := range []int64{2147483647, 2147483648} {
		t.Run(strconv.FormatInt(value, 10), func(t *testing.T) {
			if strconv.IntSize < 64 && value >= 1<<31 {
				t.Skip("the original Go int cannot represent this value")
			}
			declaration := testDeclaration()
			large := int(value)
			declaration.ConsumerContract.Search = &genregistry.ToolSearchDocument{Length: large, Terms: map[string]int{"records": large}}
			metadata := declaration.ConsumerContract.Payload
			metadata.Fields = append(metadata.Fields, &genregistry.ToolFieldMetadata{})
			metadata.Fields[0].Branches = append(metadata.Fields[0].Branches, &genregistry.ToolUnionBranch{
				Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: large}),
			})
			target := testDeclaration()
			target.Name = continuationToolName
			target.ConsumerContract.Bounds.Paging.SourceTool = &declaration.Name
			resolved, err := Resolve(testResolution(declaration, target))
			require.NoError(t, err)
			spec := resolved.Specs[tools.Ident(declaration.Name)]
			assert.Equal(t, large, spec.Search.Length)
			assert.Equal(t, large, spec.Search.Terms["records"])
			assert.NotNil(t, spec.Bounds.Paging.RetainedFields)
			assert.Empty(t, spec.Bounds.Paging.RetainedFields)
			assert.Empty(t, spec.Payload.Fields[1].Path)
			assert.Len(t, spec.Payload.Fields[0].Branches, 2)
			assert.Equal(t, tools.UntaggedUnionBranch{Index: large, JSONKind: "object"}, spec.Payload.Fields[0].Branches[1])
			binding, err := resolved.Select("company", tools.Ident(declaration.Name))
			require.NoError(t, err)
			read, err := Read(binding, nil)
			require.NoError(t, err)
			assert.Equal(t, spec.Payload.Fields, read.Specs[spec.Name].Payload.Fields)
			assert.Equal(t, spec.Search, read.Specs[spec.Name].Search)
			assert.Equal(t, resolved.Registered.RegistrationToken, read.Registered.RegistrationToken)
		})
	}

	empty := testResolution()
	empty.Toolset.Tools = []*genregistry.ToolSchema{}
	_, err := Resolve(empty)
	assert.NoError(t, err)
}
