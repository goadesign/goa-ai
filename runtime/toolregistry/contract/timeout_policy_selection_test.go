// Saved selections use generated JSON codecs. These tests verify that the
// Agent timeout promise survives persistence without changing unmarked records.
package contract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestRegistrySelectionPreservesTimeoutPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		agent  bool
		marked bool
	}{
		{name: "marked agent", agent: true, marked: true},
		{name: "unmarked agent", agent: true},
		{name: "service default"},
	} {
		t.Run(test.name, func(t *testing.T) {
			declaration := testDeclaration()
			declaration.ConsumerContract.Bounds.Paging.RetainedFields = []string{"value"}
			declaration.ConsumerContract.ReplanOnTimeout = test.marked
			if test.agent {
				declaration.ConsumerContract.Kind = agentKind
				declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{
					Executor: "reader.agent", Workflow: "reader.workflow",
					Queue: "reader.queue", Configuration: "revision/1",
				}
			}
			selection := &genregistry.RegistrySelection{
				Selection: genregistry.NewRegistrySelectionKindDirect(&genregistry.ResolvedToolset{
					Toolset: &genregistry.Toolset{
						Name: "records", RegisteredAt: "2026-09-17T00:00:00Z",
						Tools: []*genregistry.ToolSchema{declaration},
					},
					RegistrationToken: strings.Repeat("a", 64),
				}),
			}
			encoded, err := genregistry.EncodeRegistrySelection(selection)
			require.NoError(t, err)
			assert.Equal(t, test.marked, strings.Contains(string(encoded), `"ReplanOnTimeout"`))
			loaded, err := genregistry.DecodeRegistrySelection(encoded)
			require.NoError(t, err)
			direct, ok := loaded.Selection.AsDirect()
			require.True(t, ok)
			spec, err := Compile(direct.Toolset.Tools[0])
			require.NoError(t, err)
			assert.Equal(t, test.marked, spec.ReplanOnTimeout)
		})
	}
}
