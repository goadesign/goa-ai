package tests

import (
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TestGolden_ServiceAlias_Consistency checks that generated tool code imports
// an underscored service with the same package name used in Go references.
func TestGolden_ServiceAlias_Consistency(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, func() {
		// Service name contains underscore to exercise alias vs path base.
		API("catalog_agent", func() {})

		// Define a user type at API scope, referenced directly by tool payload/result.
		var Doc = Type("Doc", func() {
			Attribute("id", String, "ID")
			Required("id")
		})

		Service("catalog_agent", func() {
			Method("read", func() {
				Payload(Doc)
				Result(Doc)
			})
			Agent("reader", "", func() {
				Use("docs", func() {
					Tool("read", "Read", func() {
						Args(Doc)
						Return(Doc)
						BindTo("read")
					})
				})
			})
		})
	})

	provider := renderedFileContent(t, files, "gen/catalog_agent/toolsets/docs/provider.go")
	transforms := renderedFileContent(t, files, "gen/catalog_agent/toolsets/docs/transforms.go")
	codecs := fileContent(t, files, "gen/catalog_agent/toolsets/docs/codecs.go")
	runCompleteGeneratedPackageTest(t, files, "./gen/catalog_agent/toolsets/docs/...")
	assertGoldenGo(t, "service_alias_consistency", "provider.go.golden", provider)
	assertGoldenGo(t, "service_alias_consistency", "transforms.go.golden", transforms)
	assertGoldenGo(t, "service_alias_consistency", "codecs.go.golden", codecs)
}
