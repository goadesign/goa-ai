package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	codegen "goa.design/goa/v3/codegen"
)

func TestGolden_ServerData_UsesGeneratedCodec(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelfServerData())

	provider := generatedContentBySuffix(t, files, "toolsets/lookup/provider.go")
	require.Contains(t, provider, "ByIDAuraEvidenceServerDataCodec.ToJSON")
	require.Contains(t, provider, "InitByIDAuraEvidenceServerData(methodOut.Evidence)")
	require.NotContains(t, provider, "json.Marshal(methodOut.")

	executor := generatedContentBySuffix(t, files, "agents/scribe/lookup/service_executor.go")
	require.Contains(t, executor, "ByIDAuraEvidenceServerDataCodec.ToJSON")
	require.Contains(t, executor, "lookup.InitByIDAuraEvidenceServerData(mr.Evidence)")
	require.NotContains(t, executor, "json.Marshal(mr.")
}

func generatedContentBySuffix(t *testing.T, files []*codegen.File, suffix string) string {
	t.Helper()

	normSuffix := filepath.ToSlash(suffix)
	for _, f := range files {
		p := filepath.ToSlash(f.Path)
		if strings.HasSuffix(p, normSuffix) {
			return fileContent(t, files, p)
		}
	}

	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, filepath.ToSlash(f.Path))
	}
	require.Failf(t, "generated file not found", "suffix %q not found in generated files: %s", normSuffix, strings.Join(paths, ", "))
	return ""
}
