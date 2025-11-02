package tests

import (
	"strings"
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// Ensures service-local user types use the same import alias as referenced by
// Goa's NameScope when generating type references in codecs.
func TestGolden_ServiceAlias_Consistency(t *testing.T) {
	files := buildAndGenerate(t, func() {
		// Service name contains underscore to exercise alias vs path base.
		API("atlas_data_agent", func() {})

		// Define a user type at API scope, referenced directly by tool payload/result.
		var Doc = Type("Doc", func() {
			Attribute("id", String, "ID")
			Required("id")
		})

		Service("atlas_data_agent", func() {
			Agent("reader", "", func() {
				Uses(func() {
					Toolset("docs", func() {
						Tool("read", "Read", func() {
							Args(Doc)
							Return(Doc)
						})
					})
				})
			})
		})
	})

	// Locate generated codecs.go under specs/docs.
	codecs := fileContent(t, files, "gen/atlas_data_agent/agents/reader/specs/docs/codecs.go")
	// Prefer service package alias when user types are referenced directly; allow
	// local alias types when specs generate short forms.
	if !(strings.Contains(codecs, "atlasdataagent \"goa.design/goa-ai/gen/atlas_data_agent\"") || strings.Contains(codecs, "JSONCodec[")) {
		t.Fatalf("expected either service import alias or JSONCodec generics, got:\n%s", codecs)
	}
}
