package registrycontract

// These tests save calls to compiled tools without their declarations and
// restore them from a reader's generated declarations. They cover the restored
// contract, changes between builds, Agent targets, and the saved size.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCompiledSelectionRestoresTheSavedContract(t *testing.T) {
	for _, kind := range []string{"service", "agent"} {
		t.Run(kind, func(t *testing.T) {
			generated := compiledTestDeclarations(kind)
			registered := testResolution(compiledTestBound(generated, "revision/1")...)
			registered.Toolset.Description = new("Provider records")
			registered.Toolset.Tags = []string{"records"}
			direct, err := Resolve(registered)
			require.NoError(t, err)
			full, err := direct.Select("company", "records.change")
			require.NoError(t, err)
			compiled, err := ResolveCompiled(registered)
			require.NoError(t, err)
			small, err := compiled.Select("company", "records.change")
			require.NoError(t, err)

			fromFull, err := Read(full, nil)
			require.NoError(t, err)
			fromSmall, err := Read(small, compiledTestLookup(generated))
			require.NoError(t, err)
			assert.Equal(t, fromFull.Registered, fromSmall.Registered)
			assert.Equal(t, fromFull.Specs["records.change"].Bounds, fromSmall.Specs["records.change"].Bounds)
			assert.NotContains(t, string(small.Resolution), "payload_schema")
			assert.NotContains(t, string(small.Resolution), "unrelated")

			// A restored compiled selection selects its paging partner in the
			// same small form.
			next, err := fromSmall.Select("company", continuationToolName)
			require.NoError(t, err)
			assert.Equal(t, string(small.Resolution), string(next.Resolution))
		})
	}
}

func TestCompiledSelectionReportsChangedDeclarations(t *testing.T) {
	generated := compiledTestDeclarations("service")
	compiled, err := ResolveCompiled(testResolution(compiledTestBound(generated, "")...))
	require.NoError(t, err)
	binding, err := compiled.Select("company", "records.change")
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		change func([]*genregistry.ToolSchema) []*genregistry.ToolSchema
	}{
		{name: "changed source description", change: func(declarations []*genregistry.ToolSchema) []*genregistry.ToolSchema {
			declarations[0].Description = new("Changed")
			return declarations
		}},
		{name: "changed partner schema", change: func(declarations []*genregistry.ToolSchema) []*genregistry.ToolSchema {
			declarations[1].ResultSchema = []byte(`{"type":"string"}`)
			return declarations
		}},
		{name: "removed partner", change: func(declarations []*genregistry.ToolSchema) []*genregistry.ToolSchema {
			return declarations[:1]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := tc.change(compiledTestDeclarations("service"))
			_, err := Read(binding, compiledTestLookup(current))
			require.ErrorIs(t, err, ErrCompiledContractChanged)
		})
	}
	_, err = Read(binding, nil)
	require.ErrorContains(t, err, "requires the reader's generated declarations")
	restored, err := Read(binding, compiledTestLookup(compiledTestDeclarations("service")))
	require.NoError(t, err)
	assert.Len(t, restored.Specs, 2)
}

func TestCompiledAgentSelectionKeepsItsSelectedTarget(t *testing.T) {
	generated := compiledTestDeclarations("agent")
	first, err := ResolveCompiled(testResolution(compiledTestBound(generated, "revision/1")...))
	require.NoError(t, err)
	binding, err := first.Select("company", "records.change")
	require.NoError(t, err)
	second, err := ResolveCompiled(testResolution(compiledTestBound(generated, "revision/2")...))
	require.NoError(t, err)
	replacement, err := second.Select("company", "records.change")
	require.NoError(t, err)

	restored, err := Read(binding, compiledTestLookup(generated))
	require.NoError(t, err)
	declaration, err := restored.Declaration("records.change")
	require.NoError(t, err)
	assert.Equal(t, "revision/1", declaration.ConsumerContract.Agent.Configuration)
	assert.Nil(t, generated[0].ConsumerContract.Agent, "restoring must not change the generated declarations")
	same, err := SameSelection(binding, replacement)
	require.NoError(t, err)
	assert.False(t, same)
	same, err = SameSelection(binding, binding)
	require.NoError(t, err)
	assert.True(t, same)
}

func TestCompiledSelectionRouteAndTokenNeedNoDeclarations(t *testing.T) {
	generated := compiledTestDeclarations("service")
	registered := testResolution(compiledTestBound(generated, "")...)
	for _, tc := range []struct {
		name    string
		resolve func(*genregistry.ResolvedToolset) (*Resolution, error)
	}{
		{name: "direct", resolve: Resolve},
		{name: "compiled", resolve: ResolveCompiled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := tc.resolve(registered)
			require.NoError(t, err)
			binding, err := resolved.Select("company", "records.change")
			require.NoError(t, err)
			token, toolset, err := ServiceRoute(binding, "records.change")
			require.NoError(t, err)
			assert.Equal(t, strings.Repeat("a", 64), token)
			assert.Equal(t, "provider.records", toolset)
			_, _, err = ServiceRoute(binding, "records.unrelated")
			require.Error(t, err)
			token, err = RegistrationToken(binding)
			require.NoError(t, err)
			assert.Equal(t, strings.Repeat("a", 64), token)
		})
	}
	agent, err := ResolveCompiled(testResolution(compiledTestBound(compiledTestDeclarations("agent"), "revision/1")...))
	require.NoError(t, err)
	binding, err := agent.Select("company", "records.change")
	require.NoError(t, err)
	_, _, err = ServiceRoute(binding, "records.change")
	require.ErrorContains(t, err, "not a generated service tool")
}

func TestCompiledSelectionSizeDoesNotDependOnSchemaSize(t *testing.T) {
	generated := compiledTestDeclarations("service")
	large := []byte(`{"type":"object","description":"` + strings.Repeat("x", 512*1024) + `"}`)
	for _, declaration := range generated {
		declaration.ResultSchema = large
		declaration.ConsumerContract.Result.SchemaWithoutRootExample = large
	}
	compiled, err := ResolveCompiled(testResolution(compiledTestBound(generated, "")...))
	require.NoError(t, err)
	binding, err := compiled.Select("company", "records.change")
	require.NoError(t, err)
	assert.Less(t, len(binding.Resolution), 1024)
	_, err = Read(binding, compiledTestLookup(generated))
	require.NoError(t, err)
}

// compiledTestDeclarations returns generated declarations for a paged tool,
// its continuation, and an unrelated tool, with no provider Agent target.
func compiledTestDeclarations(kind string) []*genregistry.ToolSchema {
	source := testDeclaration()
	continuation := testDeclaration()
	continuation.Name = continuationToolName
	continuation.ConsumerContract.Bounds.Paging.SourceTool = &source.Name
	unrelated := testDeclaration()
	unrelated.Name = "records.unrelated"
	unrelated.ConsumerContract.Bounds = nil
	declarations := []*genregistry.ToolSchema{source, continuation, unrelated}
	for _, declaration := range declarations {
		declaration.ConsumerContract.Kind = kind
		if kind == agentKind {
			declaration.ConsumerContract.Confirmation = nil
		}
	}
	return declarations
}

// compiledTestBound copies generated declarations and attaches the provider
// target a registration selects for Agent tools.
func compiledTestBound(generated []*genregistry.ToolSchema, configuration string) []*genregistry.ToolSchema {
	bound := make([]*genregistry.ToolSchema, len(generated))
	for index, declaration := range generated {
		copied := *declaration
		contract := *declaration.ConsumerContract
		if contract.Kind == agentKind {
			contract.Agent = &genregistry.AgentToolTarget{Executor: "records.agent", Workflow: "records.workflow", Queue: "records.queue", Configuration: configuration}
		}
		copied.ConsumerContract = &contract
		bound[index] = &copied
	}
	return bound
}

// compiledTestLookup serves generated declarations by name.
func compiledTestLookup(declarations []*genregistry.ToolSchema) CompiledDeclarations {
	byName := make(map[tools.Ident]*CompiledDeclaration, len(declarations))
	for _, declaration := range declarations {
		compiled, err := CompileDeclaration(declaration)
		if err != nil {
			panic(err)
		}
		byName[tools.Ident(declaration.Name)] = compiled
	}
	return func(name tools.Ident) (*CompiledDeclaration, bool) {
		declaration, ok := byName[name]
		return declaration, ok
	}
}
