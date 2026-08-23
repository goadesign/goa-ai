package runtime

// These tests pin the global tool-contract registry: generated declarative
// duplicates are accepted, while later callers cannot replace the first tool
// contract, executable toolset, or mutable registration data.

import (
	"context"
	"testing"

	"goa.design/goa-ai/runtime/agent/planner"
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

func TestRegisterToolsetRejectsDuplicateExecutableOwner(t *testing.T) {
	t.Parallel()

	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	first := ToolsetRegistration{
		Name:  "svc",
		Specs: []tools.ToolSpec{spec},
		Execute: func(context.Context, *planner.ToolRequest) (*ToolExecutionResult, error) {
			return nil, nil
		},
	}
	require.NoError(t, runtime.RegisterToolset(first))

	second := first
	second.Execute = func(context.Context, *planner.ToolRequest) (*ToolExecutionResult, error) {
		return &ToolExecutionResult{}, nil
	}
	require.ErrorContains(t, runtime.RegisterToolset(second), `toolset "svc" is already registered`)
}

func TestRegisterToolsetOwnsMutableContractData(t *testing.T) {
	t.Parallel()

	const mutated = "mutated"

	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	spec.Tags = []string{"lookup"}
	spec.Payload.Schema = tools.RawJSON(`{"type":"object"}`)
	spec.Payload.FieldDescriptions = map[string]string{"query": "Lookup query."}
	registration := ToolsetRegistration{
		Name:  "svc",
		Specs: []tools.ToolSpec{spec},
		Execute: func(context.Context, *planner.ToolRequest) (*ToolExecutionResult, error) {
			return nil, nil
		},
	}
	require.NoError(t, runtime.RegisterToolset(registration))

	registration.Specs[0].Tags[0] = mutated
	registration.Specs[0].Payload.Schema[0] = '['
	registration.Specs[0].Payload.FieldDescriptions["query"] = mutated

	stored, ok := runtime.toolSpec(spec.Name)
	require.True(t, ok)
	require.Equal(t, []string{"lookup"}, stored.Tags)
	require.JSONEq(t, `{"type":"object"}`, string(stored.Payload.Schema))
	require.Equal(t, map[string]string{"query": "Lookup query."}, stored.Payload.FieldDescriptions)
}

func TestAddToolSpecsKeepsFirstCodecOwner(t *testing.T) {
	t.Parallel()

	runtime := New()
	first := newAnyJSONSpec("svc.lookup", "svc")
	first.Payload.Codec.ToJSON = func(any) ([]byte, error) {
		return []byte(`"first"`), nil
	}
	second := first
	second.Payload.Codec.ToJSON = func(any) ([]byte, error) {
		return []byte(`"second"`), nil
	}

	runtime.mu.Lock()
	runtime.addToolSpecsLocked([]tools.ToolSpec{first}, nil)
	runtime.addToolSpecsLocked([]tools.ToolSpec{second}, nil)
	runtime.mu.Unlock()

	stored, ok := runtime.toolSpec(first.Name)
	require.True(t, ok)
	encoded, err := stored.Payload.Codec.ToJSON(nil)
	require.NoError(t, err)
	require.JSONEq(t, `"first"`, string(encoded))
}
