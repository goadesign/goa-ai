// These tests generate and compile an exported agent-tool package whose name
// matches a runtime package imported by its consumer.
package tests

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGoldenAgentToolsRuntimeImportCollision checks the complete provider and
// consumer helpers, then compiles both packages with the real Go toolchain.
func TestGoldenAgentToolsRuntimeImportCollision(t *testing.T) {
	files := buildWithPrepareAndPkg(t, testscenarios.ExportedRuntimeToolset())
	provider := renderedFileContent(t, files, "gen/provider/agents/source/agenttools/runtime/helpers.go")
	consumer := renderedFileContent(t, files, "gen/consumer/agents/worker/runtime_agenttools_client.go")

	assertGoldenGo(t, "agenttools_import_collisions", "provider.go.golden", provider)
	assertGoldenGo(t, "agenttools_import_collisions", "consumer.go.golden", consumer)

	root := writeGeneratedModuleKeepingGen(t, files)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		"go",
		"test",
		"-mod=mod",
		"./gen/provider/agents/source/agenttools/runtime",
		"./gen/consumer/agents/worker",
	)
	runGeneratedGoTestCommand(t, root, command)
	require.NoError(t, ctx.Err())
}
