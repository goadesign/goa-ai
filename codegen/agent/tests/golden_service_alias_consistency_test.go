package tests

import (
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TestGolden_ServiceAlias_Consistency checks that generated JSON code imports
// a service type with the same package name used in its Go reference.
func TestGolden_ServiceAlias_Consistency(t *testing.T) {
	files := buildAndGenerate(t, func() {
		// Service name contains underscore to exercise alias vs path base.
		API("catalog_agent", func() {})

		// Define a user type at API scope, referenced directly by tool payload/result.
		var Doc = Type("Doc", func() {
			Attribute("id", String, "ID")
			Required("id")
		})

		Service("catalog_agent", func() {
			Agent("reader", "", func() {
				Use("docs", func() {
					Tool("read", "Read", func() {
						Args(Doc)
						Return(Doc)
					})
				})
			})
		})
	})

	// Compare generated codecs.go under tools/docs against golden.
	codecs := fileContent(t, files, "gen/catalog_agent/toolsets/docs/codecs.go")
	assertGoldenGo(t, "service_alias_consistency", "codecs.go.golden", codecs)
}
