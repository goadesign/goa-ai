package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGolden_SidecarNilEncodesNull(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelfServerDataPointerSidecar())

	codecs := generatedContentBySuffix(t, files, "toolsets/lookup/codecs.go")
	require.Contains(t, codecs, `return []byte("null"), nil`)
	require.Contains(t, codecs, "MarshalByIDAuraChartServerData")
	require.NotContains(t, codecs, `return nil, fmt.Errorf("byIDAuraChartServerData is nil")`)
}
