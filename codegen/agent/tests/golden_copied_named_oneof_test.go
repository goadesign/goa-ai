// Package tests verifies that copied result types keep the field layouts of
// shared externally located unions.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenCopiedNamedOneOf(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.CopiedNamedOneOf())
	transportTypes := renderedFileContent(t, files, "gen/records/toolsets/records/http/types.go")
	transportUnions := renderedFileContent(t, files, "gen/records/toolsets/records/http/unions.go")
	codecs := renderedFileContent(t, files, "gen/records/toolsets/records/codecs.go")
	const helperMarker = "// Helper transform functions"
	_, helpers, found := strings.Cut(codecs, helperMarker)
	require.True(t, found, "generated codecs have no helper section")

	require.Equal(t, 1, strings.Count(transportTypes, "SharedFilterTransport struct {"))
	require.Equal(t, 2, strings.Count(helpers, "res.Filter = encodeSharedFilterToSharedFilterTransport(v.Filter)"))
	require.Equal(t, 2, strings.Count(helpers, "res.Filter = decodeSharedFilterTransportToSharedFilter(v.Filter)"))
	helpers = "package records\n\n" + helperMarker + helpers

	assertGoldenGo(t, "copied_named_oneof", "transport_types.go.golden", transportTypes)
	assertGoldenGo(t, "copied_named_oneof", "transport_unions.go.golden", transportUnions)
	assertGoldenGo(t, "copied_named_oneof", "codec_helpers.go.golden", helpers)
	runCompleteGeneratedPackageTest(t, files, "./gen/records/toolsets/records/...")
}
