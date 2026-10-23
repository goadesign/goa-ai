package registrycontract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestPreviousSelectionUsesExactOwnerDeclarations(t *testing.T) {
	for _, kind := range []string{"service", "agent"} {
		for _, change := range []string{"selected description", "unrelated description", "wrong route", "wrong token", "changed old declaration", "missing old operation", "wrong target"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				if kind == "service" && change == "wrong target" {
					t.Skip("service selections have no Agent target")
				}
				generated := compiledTestDeclarations(kind)
				owned := testResolution(compiledTestBound(generated, "revision/1")...)
				saved, err := ResolveCompiled(owned)
				require.NoError(t, err)
				binding, err := saved.Select("company", "records.change")
				require.NoError(t, err)
				before := append([]byte(nil), binding.Resolution...)
				current := compiledTestDeclarations(kind)
				current[0].Description = new("A clearer current operation description")
				_, err = Read(binding, compiledTestLookup(current))
				require.ErrorIs(t, err, ErrCompiledContractChanged)
				switch change {
				case "selected description":
				case "unrelated description":
					owned.Toolset.Tools[2].Description = new("Changed unrelated operation")
				case "wrong route":
					owned.Toolset.Name = "another.resource"
				case "wrong token":
					owned.RegistrationToken = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
				case "changed old declaration":
					owned.Toolset.Tools[0].Description = new("Not the saved operation")
				case "missing old operation":
					owned.Toolset.Tools = owned.Toolset.Tools[1:]
				case "wrong target":
					owned.Toolset.Tools[0].ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "foreign.agent", Workflow: "foreign.workflow", Queue: "foreign.queue", Configuration: "revision/other"}
				}
				prior, err := ReadPrevious(binding, owned)
				if change == "selected description" || change == "unrelated description" {
					require.NoError(t, err)
					assert.Len(t, prior.Specs, 2)
				} else {
					require.Error(t, err)
				}
				assert.Equal(t, before, []byte(binding.Resolution))
			})
		}
	}
}
