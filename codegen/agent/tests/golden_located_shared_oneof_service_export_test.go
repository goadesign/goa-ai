// Package tests verifies that a located shared result keeps one union
// declaration when both a service and its exported tool use it.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenLocatedSharedOneOfServiceExport(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.LocatedSharedOneOfServiceExport())
	sharedTypes := renderedFileContent(t, files, "gen/types/cycle_facts.go")
	sharedUnions := renderedFileContent(t, files, "gen/types/unions.go")
	service := renderedFileContent(t, files, "gen/records/service.go")
	toolTypes := renderedFileContent(t, files, "gen/records/toolsets/read/types.go")

	require.Equal(t, 1, strings.Count(sharedUnions, "type Conclusion struct {"))
	require.Contains(t, sharedTypes, "Conclusion Conclusion")
	require.Contains(t, service, "Results []*types.CycleFacts")
	require.Contains(t, toolTypes, "Results []*types.CycleFacts")
	require.False(t, fileExists(files, "gen/records/unions.go"))
	require.False(t, fileExists(files, "gen/records/toolsets/read/unions.go"))

	assertGoldenGo(t, "located_shared_oneof_service_export", "types.go.golden", sharedTypes)
	assertGoldenGo(t, "located_shared_oneof_service_export", "unions.go.golden", sharedUnions)
	assertGoldenGo(t, "located_shared_oneof_service_export", "service.go.golden", service)
	assertGoldenGo(t, "located_shared_oneof_service_export", "tool_types.go.golden", toolTypes)
	runCompleteGeneratedPackageTest(t, files, "./gen/...")
}
