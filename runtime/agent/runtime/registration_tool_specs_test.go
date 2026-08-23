package runtime

// These tests pin the global tool-contract registry: generated duplicates are
// accepted, while the same tool name cannot acquire different planner text,
// schemas, policy metadata, or executable codecs.

import (
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"

	"github.com/stretchr/testify/require"
)

func TestValidateToolSpecRegistrationsRejectsConflictingContract(t *testing.T) {
	t.Parallel()

	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	runtime.mu.Lock()
	runtime.addToolSpecsLocked([]tools.ToolSpec{spec}, nil)
	runtime.mu.Unlock()

	require.NoError(t, runtime.validateToolSpecRegistrations(toolSpecRegistration{
		specs: []tools.ToolSpec{spec},
	}))

	changed := spec
	changed.Description = "different planner contract"
	require.ErrorContains(t, runtime.validateToolSpecRegistrations(toolSpecRegistration{
		specs: []tools.ToolSpec{changed},
	}), `tool "svc.lookup" is already registered with a different contract`)
}
