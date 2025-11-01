package tests

import (
    "testing"

    "github.com/stretchr/testify/require"
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
    // Import path must reference the service package path and alias must be the package name.
    require.Contains(t, codecs, "atlasdataagent \"goa.design/goa-ai/gen/atlas_data_agent\"")
    // Typed JSONCodec should reference the fully-qualified service type using the same alias.
    require.Contains(t, codecs, "JSONCodec[*atlasdataagent.Doc]")
}


