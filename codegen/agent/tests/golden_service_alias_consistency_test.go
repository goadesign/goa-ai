package tests

import (
	"testing"

	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TestGolden_ServiceAlias_Consistency checks that generated JSON code imports
// a service type with the same package name used in its Go reference.
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
			aidsl.Agent("reader", "", func() {
				aidsl.Use("docs", func() {
					aidsl.Tool("read", "Read", func() {
						aidsl.Args(Doc)
						aidsl.Return(Doc)
					})
				})
			})
		})
	})

	// Compare generated codecs.go under tools/docs against golden.
	codecs := fileContent(t, files, "gen/atlas_data_agent/toolsets/docs/codecs.go")
	assertGoldenGo(t, "service_alias_consistency", "codecs.go.golden", codecs)
}
