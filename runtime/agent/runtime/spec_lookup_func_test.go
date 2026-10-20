package runtime

// This file adapts a test function to the lookup interface that runtime code
// uses for static tool contracts and their registry declarations.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// specLookupFunc serves specifications from a test function and has no
	// generated registry declarations.
	specLookupFunc func(tools.Ident) (tools.ToolSpec, bool)
)

// spec returns the function's specification for one tool name.
func (f specLookupFunc) spec(name tools.Ident) (tools.ToolSpec, bool) {
	return f(name)
}

// registryDeclaration reports that a test lookup has no generated declarations.
func (specLookupFunc) registryDeclaration(tools.Ident) (*genregistry.ToolSchema, bool) {
	return nil, false
}

// testDeclarations returns a lookup over generated-style declarations, as the
// aggregate ToolSchema function of a generated agent would.
func testDeclarations(declarations []*genregistry.ToolSchema) func(tools.Ident) (*genregistry.ToolSchema, bool) {
	byName := make(map[tools.Ident]*genregistry.ToolSchema, len(declarations))
	for _, declaration := range declarations {
		byName[tools.Ident(declaration.Name)] = declaration
	}
	return func(name tools.Ident) (*genregistry.ToolSchema, bool) {
		declaration, ok := byName[name]
		return declaration, ok
	}
}

// testDirectSelection saves a call to name with its full declaration, as
// releases before compiled selections did.
func testDirectSelection(t *testing.T, selected *genregistry.ResolvedToolset, name tools.Ident) *tools.RegistryBinding {
	t.Helper()
	resolved, err := registrycontract.Resolve(selected)
	require.NoError(t, err)
	binding, err := resolved.Select(testRegistryName, name)
	require.NoError(t, err)
	return binding
}
