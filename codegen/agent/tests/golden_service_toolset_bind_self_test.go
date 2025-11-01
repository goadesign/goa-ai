package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Method-backed toolset generates service_toolset.go with adapters and ToolMeta.
func TestGolden_ServiceToolset_BindSelf(t *testing.T) {
	design := testscenarios.ServiceToolsetBindSelf()
	files := buildAndGenerate(t, design)
	svcToolset := fileContent(t, files, "gen/alpha/agents/scribe/lookup/service_toolset.go")
	assertGoldenGo(t, "service_toolset_bind_self", "service_toolset.go.golden", svcToolset)
}
