package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
)

// TestTransformsHeader_NoDuplicate ensures transforms.go renders a single
// header/package/import block (i.e., template does not duplicate the header).
func TestTransformsHeader_NoDuplicate(t *testing.T) {
	files := buildAndGenerate(t, func() {
		goadsl.API("svc", func() {})
		var QPayload = goadsl.Type("QPayload", func() { goadsl.Attribute("q", goadsl.String, "Q"); goadsl.Required("q") })
		var OkResult = goadsl.Type("OkResult", func() { goadsl.Attribute("ok", goadsl.Boolean, "OK") })
		goadsl.Service("svc", func() {
			goadsl.Method("Do", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("q", goadsl.String, "Q")
					goadsl.Required("q")
				})
				goadsl.Result(func() { goadsl.Attribute("ok", goadsl.Boolean, "OK") })
			})
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("lookup", func() {
						Tool("by_id", "Lookup by ID", func() {
							Args(QPayload)
							Return(OkResult)
							BindTo("Do")
						})
					})
				})
			})
		})
	})

	p := filepath.ToSlash("gen/svc/agents/scribe/specs/lookup/transforms.go")
	content := fileContent(t, files, p)
	// Quick sanity: must compile as Go code per golden below
	require.Contains(t, content, "package lookup\n", "expected single package decl")
	// Compare to golden
	assertGoldenGo(t, "transforms_header", "transforms.go.golden", content)
}
