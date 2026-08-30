// Package tests verifies that conversion helpers retain the exact order Goa
// selected after comparing all generated result layouts.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenTransformHelperGroupOrder(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.TransformHelperGroupOrder())
	codecs := renderedFileContent(t, files, "gen/records/toolsets/records/codecs.go")
	// The old generator failed while naming this section. Keep the golden small,
	// then compile the complete package below to check every generated reference.
	const marker = "// Helper transform functions"
	_, helpers, found := strings.Cut(codecs, marker)
	require.True(t, found, "generated codecs have no helper section")
	helpers = "package records\n\n" + marker + helpers

	assertGoldenGo(t, "transform_helper_group_order", "codec_helpers.go.golden", helpers)
	runCompleteGeneratedPackageTest(t, files, "./gen/records/toolsets/records/...")
}
