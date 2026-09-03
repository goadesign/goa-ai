package runtime

// These tests pin the global tool-contract registry: generated declarative
// duplicates are accepted, while later callers cannot replace the first tool
// contract, executable toolset, or mutable registration data.

import (
	"context"
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"

	"github.com/stretchr/testify/require"
)

func TestValidateToolSpecRegistrationsRejectsConflictingContract(t *testing.T) {
	t.Parallel()

	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	runtime.mu.Lock()
	runtime.addToolSpecsLocked([]tools.ToolSpec{spec}, nil, mustToolDefinitions([]tools.ToolSpec{spec}))
	runtime.mu.Unlock()

	_, err := runtime.validateToolSpecRegistrations(toolSpecRegistration{
		specs: []tools.ToolSpec{spec},
	})
	require.NoError(t, err)

	changed := spec
	changed.Description = "different planner contract"
	_, err = runtime.validateToolSpecRegistrations(toolSpecRegistration{
		specs: []tools.ToolSpec{changed},
	})
	require.ErrorContains(t, err, `tool "svc.lookup" is already registered with a different contract`)
}

func TestValidateToolSpecRegistrationsRejectsIncompleteResultContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result tools.TypeSpec
		want   string
	}{
		{name: "no result"},
		{name: "complete result", result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
		{
			name: "encoder only",
			result: tools.TypeSpec{Codec: tools.JSONCodec[any]{
				ToJSON: tools.AnyJSONCodec.ToJSON,
			}},
			want: "result codec must define both ToJSON and FromJSON",
		},
		{
			name: "decoder only",
			result: tools.TypeSpec{Codec: tools.JSONCodec[any]{
				FromJSON: tools.AnyJSONCodec.FromJSON,
			}},
			want: "result codec must define both ToJSON and FromJSON",
		},
		{
			name:   "metadata without codec",
			result: tools.TypeSpec{Name: "result"},
			want:   "result metadata requires a complete codec",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := New(newTestStore())
			spec := newAnyJSONSpec("svc.tool")
			spec.Result = test.result
			_, err := runtime.validateToolSpecRegistrations(toolSpecRegistration{
				specs: []tools.ToolSpec{spec},
			})
			if test.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.want)
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestRegisterToolsetRejectsInvalidPayloadContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload tools.TypeSpec
		want    string
	}{
		{
			name: "malformed schema",
			payload: tools.TypeSpec{
				Schema: tools.RawJSON(`{"type":`),
				Codec:  tools.AnyJSONCodec,
			},
			want: "invalid input schema",
		},
		{
			name: "missing decoder",
			payload: tools.TypeSpec{
				Schema: tools.RawJSON(`{"type":"object"}`),
			},
			want: "payload decoder is required",
		},
		{
			name: "example rejected by decoder",
			payload: tools.TypeSpec{
				Schema:                   tools.RawJSON(`{"type":"object"}`),
				SchemaWithoutRootExample: tools.RawJSON(`{"type":"object"}`),
				ExampleJSON:              tools.RawJSON(`{"query":"status"}`),
				Codec: tools.JSONCodec[any]{
					FromJSON: func([]byte) (any, error) {
						return nil, errors.New("example is not decodable")
					},
				},
			},
			want: "example JSON does not satisfy its payload contract",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := New(newTestStore())
			err := runtime.RegisterToolset(ToolsetRegistration{
				Name: "svc",
				Specs: []tools.ToolSpec{{
					Name:    "svc.tool",
					Payload: test.payload,
				}},
				Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
					return nil, nil
				},
			})

			require.ErrorIs(t, err, ErrInvalidConfig)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestRegisterToolsetRejectsDuplicateExecutableOwner(t *testing.T) {
	t.Parallel()

	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	first := ToolsetRegistration{
		Name:  "svc",
		Specs: []tools.ToolSpec{spec},
		Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
			return nil, nil
		},
	}
	require.NoError(t, runtime.RegisterToolset(first))

	second := first
	second.Execute = func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
		return &ToolExecutionResult{}, nil
	}
	require.ErrorContains(t, runtime.RegisterToolset(second), `toolset "svc" is already registered`)
}

func TestRegisterToolsetRejectsDistinctRoutesForOneTool(t *testing.T) {
	t.Parallel()

	runtime := New(newTestStore())
	spec := newAnyJSONSpec("shared.lookup")
	execute := func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
		return nil, nil
	}
	require.NoError(t, runtime.RegisterToolset(ToolsetRegistration{
		Name:    "alpha.shared",
		Specs:   []tools.ToolSpec{spec},
		Execute: execute,
	}))

	err := runtime.RegisterToolset(ToolsetRegistration{
		Name:    "beta.shared",
		Specs:   []tools.ToolSpec{spec},
		Execute: execute,
	})
	require.ErrorContains(t, err, `tool "shared.lookup" is already executed by toolset "alpha.shared"`)
}

func TestRegisterToolsetOwnsMutableContractData(t *testing.T) {
	t.Parallel()

	const mutated = "mutated"

	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	spec.Tags = []string{"lookup"}
	spec.Payload.Schema = tools.RawJSON(`{"type":"object"}`)
	spec.Payload.FieldDescriptions = map[string]string{"query": "Lookup query."}
	registration := ToolsetRegistration{
		Name:  "svc",
		Specs: []tools.ToolSpec{spec},
		Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
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

func TestToolsetRegistrationOwnsResultMaterializerRoute(t *testing.T) {
	t.Parallel()

	called := false
	runtime := New(newTestStore())
	spec := sharedRouteTestSpec()
	registration := ToolsetRegistration{
		Name:  "beta.shared",
		Specs: []tools.ToolSpec{spec},
		Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
			return nil, nil
		},
		ResultMaterializer: func(context.Context, ToolCallMeta, *ToolCall, *planner.ToolResult) error {
			called = true
			return nil
		},
	}
	require.NoError(t, runtime.RegisterToolset(registration))

	call := ToolCall{Name: spec.Name}
	result := &planner.ToolResult{Name: spec.Name, Result: "pong"}
	require.NoError(t, runtime.applyResultMaterializer(context.Background(), spec, call, result))
	require.True(t, called)
}

func TestToolsetRegistrationOwnsExecutionRoute(t *testing.T) {
	t.Parallel()

	executed := false
	runtime := New(newTestStore())
	spec := sharedRouteTestSpec()
	require.NoError(t, runtime.RegisterToolset(ToolsetRegistration{
		Name:  "beta.shared",
		Specs: []tools.ToolSpec{spec},
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			executed = true
			return Executed(&planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     "pong",
			}), nil
		},
	}))

	result, err := runtime.ExecuteToolActivity(context.Background(), &ToolInput{
		RunID:       "run-1",
		AgentID:     "beta.worker",
		ToolsetName: "beta.shared",
		ToolName:    spec.Name,
		ToolCallID:  "call-1",
		Payload:     []byte(`{"message":"ping"}`),
	})
	require.NoError(t, err)
	require.True(t, executed)
	require.JSONEq(t, `"pong"`, string(result.Payload))
}

func TestToolSpecAccessorsReturnDetachedSnapshots(t *testing.T) {
	t.Parallel()

	const agentID agent.Ident = "svc.agent"

	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	spec.Tags = []string{"lookup"}
	spec.Meta = map[string][]string{"owner": {"service"}}
	spec.Payload.Schema = tools.RawJSON(`{"type":"object"}`)
	spec.Payload.FieldDescriptions = map[string]string{"query": "Lookup query."}
	spec.Bounds = &tools.BoundsSpec{
		Paging: &tools.PagingSpec{CursorField: "cursor"},
	}
	spec.Confirmation = &tools.ConfirmationSpec{Title: "Confirm lookup"}
	spec.ServerData = []*tools.ServerDataSpec{
		{
			Kind: "evidence",
			Type: tools.TypeSpec{
				Schema: tools.RawJSON(`{"type":"object"}`),
			},
		},
	}

	runtime.mu.Lock()
	runtime.toolSpecs[spec.Name] = spec
	runtime.agentToolSpecs[agentID] = []tools.ToolSpec{spec}
	runtime.mu.Unlock()

	t.Run("tool", func(t *testing.T) {
		assertToolSpecReaderDetached(t, func() tools.ToolSpec {
			stored, ok := runtime.ToolSpec(spec.Name)
			require.True(t, ok)
			return stored
		})
	})
	t.Run("agent", func(t *testing.T) {
		assertToolSpecReaderDetached(t, func() tools.ToolSpec {
			stored := runtime.ToolSpecsForAgent(agentID)
			require.Len(t, stored, 1)
			return stored[0]
		})
	})
}

func TestAddToolSpecsKeepsFirstCodecOwner(t *testing.T) {
	t.Parallel()

	runtime := New(newTestStore())
	first := newAnyJSONSpec("svc.lookup")
	first.Payload.Codec.ToJSON = func(any) ([]byte, error) {
		return []byte(`"first"`), nil
	}
	second := first
	second.Payload.Codec.ToJSON = func(any) ([]byte, error) {
		return []byte(`"second"`), nil
	}

	runtime.mu.Lock()
	runtime.addToolSpecsLocked([]tools.ToolSpec{first}, nil, mustToolDefinitions([]tools.ToolSpec{first}))
	runtime.addToolSpecsLocked([]tools.ToolSpec{second}, nil, mustToolDefinitions([]tools.ToolSpec{second}))
	runtime.mu.Unlock()

	stored, ok := runtime.toolSpec(first.Name)
	require.True(t, ok)
	encoded, err := stored.Payload.Codec.ToJSON(nil)
	require.NoError(t, err)
	require.JSONEq(t, `"first"`, string(encoded))
}

// sharedRouteTestSpec returns a shared contract that can be bound by any local
// toolset registration.
func sharedRouteTestSpec() tools.ToolSpec {
	return newAnyJSONSpec("shared.ping")
}

// assertToolSpecReaderDetached proves that callers cannot mutate any nested
// declarative contract data through a public runtime accessor.
func assertToolSpecReaderDetached(t *testing.T, read func() tools.ToolSpec) {
	t.Helper()

	const mutated = "mutated"

	returned := read()
	returned.Tags[0] = mutated
	returned.Meta["owner"][0] = mutated
	returned.Payload.Schema[0] = '['
	returned.Payload.FieldDescriptions["query"] = mutated
	returned.Bounds.Paging.CursorField = mutated
	returned.Confirmation.Title = mutated
	returned.ServerData[0].Kind = mutated
	returned.ServerData[0].Type.Schema[0] = '['

	stored := read()
	require.Equal(t, []string{"lookup"}, stored.Tags)
	require.Equal(t, map[string][]string{"owner": {"service"}}, stored.Meta)
	require.JSONEq(t, `{"type":"object"}`, string(stored.Payload.Schema))
	require.Equal(t, map[string]string{"query": "Lookup query."}, stored.Payload.FieldDescriptions)
	require.Equal(t, "cursor", stored.Bounds.Paging.CursorField)
	require.Equal(t, "Confirm lookup", stored.Confirmation.Title)
	require.Equal(t, "evidence", stored.ServerData[0].Kind)
	require.JSONEq(t, `{"type":"object"}`, string(stored.ServerData[0].Type.Schema))
}
