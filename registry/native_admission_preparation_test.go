// Native admission must preserve current generated declarations and the saved
// reader's fingerprints, including paging groups and escaped metadata.
package registry

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestNativeAdmissionPreparationPreservesGeneratedEncoding(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		build func() *genregistry.AgentToolsetDeclaration
	}{
		{"ordinary", func() *genregistry.AgentToolsetDeclaration {
			return testAgentToolset("v1")
		}},
		{"paging", testAgentPagingToolset},
		{"escaped", func() *genregistry.AgentToolsetDeclaration {
			v := testAgentToolset("v1")
			description := "A <record> & \"quoted\" value\n雪"
			v.Description = &description
			v.Tags = []string{"one", "雪"}
			v.Tools[0].Description = &description
			v.Tools[0].ConsumerContract.Title = description
			return v
		}},
		{"empty_metadata", func() *genregistry.AgentToolsetDeclaration {
			v := testAgentToolset("v1")
			v.Tags = []string{}
			v.Tools[0].Tags = []string{}
			v.Tools[0].ConsumerContract.Meta = map[string][]string{"nil": nil, "empty": {}}
			v.Tools[0].ConsumerContract.RequiredLabels = []string{}
			v.Tools[0].ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{}
			return v
		}},
		{"map_order", func() *genregistry.AgentToolsetDeclaration {
			v := testAgentToolset("v1")
			v.Tools[0].ConsumerContract.Meta = make(map[string][]string)
			for _, key := range []string{"雪", "z", "<a>", "a"} {
				v.Tools[0].ConsumerContract.Meta[key] = []string{key}
			}
			return v
		}},
		{"all_union_branches", func() *genregistry.AgentToolsetDeclaration {
			v := testAgentToolset("v1")
			path := []*genregistry.ToolFieldPathSegment{
				{Segment: genregistry.NewToolFieldSegmentField("")},
				{Segment: genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})},
			}
			v.Tools[0].ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{
				Path: path,
				Branches: []*genregistry.ToolUnionBranch{
					{Selection: genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{Discriminator: path, Value: "selected"})},
					{Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{Path: path, JSONKind: "object", Index: 0})},
				},
			}}
			return v
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			v := fixture.build()
			p := &genregistry.AgentExportsDeclaration{Scope: "owner", ResourceName: "native", Name: v.Name, Description: v.Description, Version: v.Version, Tags: v.Tags, Exports: v.Tools}
			require.NoError(t, genregistry.ValidateAgentExportsDeclarationValue(p))
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			catalog := newToolsetCatalog(newTestCatalogMap(clock), clock)
			service := &Service{catalog: catalog, validator: catalog.validator}
			toolset := &genregistry.Toolset{Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Exports}
			expected, err := json.Marshal(toolset)
			require.NoError(t, err)
			prepared, err := service.prepareAgentToolset(toolset, &CatalogIdentity{Scope: p.Scope, Name: p.ResourceName})
			require.NoError(t, err)
			assert.Equal(t, string(expected), prepared.raw)
			decoded, fingerprint, err := internaladmission.SavedToolsetFingerprint([]byte(prepared.raw))
			require.NoError(t, err)
			assert.Equal(t, prepared.fingerprint, fingerprint)
			preparedDecoded, err := prepared.decode("")
			require.NoError(t, err)
			assert.Equal(t, preparedDecoded, decoded)
			read, err := catalog.validatedDefinition(t.Context(), p.Name, prepared.raw, true)
			require.NoError(t, err)
			assert.Equal(t, prepared.fingerprint, read.fingerprint)
			assert.Equal(t, prepared.info, read.info)
			originalTitle := preparedDecoded.Tools[0].ConsumerContract.Title
			p.Exports[0].ConsumerContract.Title = "Caller mutation"
			p.Exports[0].PayloadSchema[0] = ' '
			contract := p.Exports[0].ConsumerContract
			if contract.Meta != nil {
				contract.Meta["caller"] = []string{"changed"}
			}
			for _, field := range contract.Payload.Fields {
				for _, segment := range field.Path {
					segment.Segment.SetField("changed")
				}
				for _, branch := range field.Branches {
					if tagged, ok := branch.Selection.AsTagged(); ok {
						tagged.Value = "changed"
					}
					if untagged, ok := branch.Selection.AsUntagged(); ok {
						untagged.Index = 1
					}
				}
			}
			afterMutate, err := prepared.decode("")
			require.NoError(t, err)
			assert.Equal(t, originalTitle, afterMutate.Tools[0].ConsumerContract.Title)
			assert.Equal(t, decoded, afterMutate)
			for name, schema := range prepared.executionSchemas {
				assert.Same(t, schema, read.executionSchemas[name])
			}
		})
	}
}
