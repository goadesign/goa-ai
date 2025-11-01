package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agents/tests/testscenarios"
)

// Method-backed toolset bound to a different service.
func TestGolden_ServiceToolset_BindCross(t *testing.T) {
	design := testscenarios.ServiceToolsetBindCross()
	files := buildAndGenerate(t, design)
	svcToolset := fileContent(t, files, "gen/alpha/agents/scribe/lookup/service_toolset.go")
	assertGoldenGo(t, "service_toolset_bind_cross", "service_toolset.go.golden", svcToolset)
}
