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
	require.Contains(t, provider, "ByIDRecordsEvidenceServerDataCodec().ToJSON")
	require.Contains(t, provider, "InitByIDRecordsEvidenceServerData(methodOut.Evidence)")
	require.NotContains(t, provider, "json.Marshal(methodOut.")

	specs := generatedContentBySuffix(t, files, "toolsets/lookup/specs.go")
	require.Contains(t, specs, "CanonicalizeServerData: canonicalizeByIDServerData")
	require.Contains(t, specs, "toolserverdata.Canonicalize(data, canonicalizeByIDServerDataItem)")
	require.Contains(t, specs, `case "records.evidence":`)
	require.Contains(t, specs, "byIDRecordsEvidenceServerDataCodec.FromJSON(data)")
	require.Contains(t, specs, "byIDRecordsEvidenceServerDataCodec.ToJSON(value)")
	require.NotContains(t, specs, "spec.ServerData")
	require.NotContains(t, specs, "tools.ServerDataItem")

	executor := generatedContentBySuffix(t, files, "agents/scribe/lookup/service_executor.go")
	require.Contains(t, executor, "ByIDRecordsEvidenceServerDataCodec().ToJSON")
	require.Contains(t, executor, "lookupspecs.InitByIDRecordsEvidenceServerData(mr.Evidence)")
	require.NotContains(t, executor, "json.Marshal(mr.")
	require.Contains(t, executor, "var serverData rawjson.Message")
	require.Contains(t, executor, "serverData = rawjson.Message(b)")
	require.NotContains(t, executor, "rawjson.RawJSON")
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
