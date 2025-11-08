package tests

import (
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

	// Compare generated codecs.go under specs/docs against golden.
	codecs := fileContent(t, files, "gen/atlas_data_agent/agents/reader/specs/docs/codecs.go")
	assertGoldenGo(t, "service_alias_consistency", "codecs.go.golden", codecs)
}
