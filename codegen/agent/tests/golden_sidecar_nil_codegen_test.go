package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGolden_ServerDataNilEncodesNull(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelfServerDataOptional())

	codecs := generatedContentBySuffix(t, files, "toolsets/lookup/codecs.go")
	require.Contains(t, codecs, `return []byte("null"), nil`)
	require.Contains(t, codecs, "MarshalByIDChartsPreviewServerData")
	require.NotContains(t, codecs, `return nil, fmt.Errorf("byIDChartsPreviewServerData is nil")`)
}
