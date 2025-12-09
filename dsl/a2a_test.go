package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
)

// **Feature: a2a-architecture-redesign, Property 1: A2A DSL Default Suite ID**
// **Validates: Requirements 1.2**
//
// For any exported toolset with A2A configuration and no explicit Suite, the
// generated suite ID should equal "<service>.<agent>.<toolset>" and defaults
// should be applied for Path ("/a2a") and Version ("1.0").
func TestA2ADefaultSuiteForAgentExport(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				Export("tools", func() {
					A2A()
					Tool("t1", "tool one", func() {})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	a := agentsexpr.Root.Agents[0]
	require.NotNil(t, a.Exported)
	require.Len(t, a.Exported.Toolsets, 1)
	ts := a.Exported.Toolsets[0]
	require.NotNil(t, ts.A2A)
	require.Equal(t, "svc.agent.tools", ts.A2A.Suite)
	require.Equal(t, "/a2a", ts.A2A.Path)
	require.Equal(t, "1.0", ts.A2A.Version)
}
