// Package mcp verifies the expression data exposed to Goa-AI generators.
package mcp

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRootContainsOnlyServerDeclarations prevents method-backed prompts from
// returning as a second prompt definition path beside MCP server prompts.
func TestRootContainsOnlyServerDeclarations(t *testing.T) {
	rootType := reflect.TypeOf(RootExpr{})
	_, hasDynamicPrompts := rootType.FieldByName("DynamicPrompts")
	require.False(t, hasDynamicPrompts)
}

// TestMCPServerStoresOnlyAuthoredProtocolChoices prevents generated behavior
// from being represented as a second, configurable design value.
func TestMCPServerStoresOnlyAuthoredProtocolChoices(t *testing.T) {
	mcpType := reflect.TypeOf(MCPExpr{})
	_, hasTransport := mcpType.FieldByName("Transport")
	_, hasCapabilities := mcpType.FieldByName("Capabilities")
	require.False(t, hasTransport)
	require.False(t, hasCapabilities)
}
