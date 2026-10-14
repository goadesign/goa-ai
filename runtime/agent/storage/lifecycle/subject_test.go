// Child-link fixtures narrow the generated subject before constructing the
// Agent-only child identity required by lifecycle records.
package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
)

// subjectAgent verifies that a child-link fixture names a real Agent before
// passing its identifier to the Agent-only child contract.
func subjectAgent(t *testing.T, subject genrunidentity.RunSubject) agent.Ident {
	t.Helper()
	id, ok := subject.Executor.AsAgent()
	require.True(t, ok)
	return agent.Ident(id)
}
