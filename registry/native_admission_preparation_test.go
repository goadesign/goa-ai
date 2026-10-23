// Native admission must preserve current generated declarations and the saved
// reader's fingerprints, including paging groups and escaped metadata.
package registry

import (
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
			return v
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			v := fixture.build()
			p := &genregistry.AgentExportsDeclaration{Scope: "owner", ResourceName: "native", Name: v.Name, Description: v.Description, Version: v.Version, Tags: v.Tags, Exports: v.Tools}
			_, err := genregistry.EncodeAgentExportsDeclaration(p)
			require.NoError(t, err)
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			catalog := newToolsetCatalog(newTestCatalogMap(clock), clock)
			service := &Service{catalog: catalog, validator: catalog.validator}
			prepared, err := service.prepareAgentToolset(&genregistry.Toolset{Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Exports}, &CatalogIdentity{Scope: p.Scope, Name: p.ResourceName})
			require.NoError(t, err)
			decoded, fingerprint, err := internaladmission.SavedToolsetFingerprint([]byte(prepared.raw))
			require.NoError(t, err)
			assert.Equal(t, prepared.fingerprint, fingerprint)
			assert.Equal(t, prepared.declaration, decoded)
			read, err := catalog.validatedDefinition(t.Context(), p.Name, prepared.raw, true)
			require.NoError(t, err)
			assert.Equal(t, prepared.fingerprint, read.fingerprint)
			assert.Equal(t, prepared.declaration, read.declaration)
			assert.Equal(t, prepared.info, read.info)
			for name, schema := range prepared.executionSchemas {
				assert.Same(t, schema, read.executionSchemas[name])
			}
		})
	}
}
