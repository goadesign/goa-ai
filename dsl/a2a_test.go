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

// TestA2APathOverride verifies A2APath overrides the default path.
func TestA2APathOverride(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				Export("tools", func() {
					A2A(func() {
						A2APath("/custom/a2a")
					})
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
	require.Equal(t, "/custom/a2a", ts.A2A.Path)
}

// TestA2AVersionOverride verifies A2AVersion overrides the default version.
func TestA2AVersionOverride(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				Export("tools", func() {
					A2A(func() {
						A2AVersion("2.0")
					})
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
	require.Equal(t, "2.0", ts.A2A.Version)
}

// TestA2ASuiteOverride verifies Suite overrides the default suite ID.
func TestA2ASuiteOverride(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				Export("tools", func() {
					A2A(func() {
						Suite("custom.suite.id")
					})
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
	require.Equal(t, "custom.suite.id", ts.A2A.Suite)
}

// TestA2AFullConfiguration verifies all A2A settings can be configured together.
func TestA2AFullConfiguration(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				Export("tools", func() {
					A2A(func() {
						Suite("my.custom.suite")
						A2APath("/api/a2a")
						A2AVersion("2.5")
					})
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
	require.Equal(t, "my.custom.suite", ts.A2A.Suite)
	require.Equal(t, "/api/a2a", ts.A2A.Path)
	require.Equal(t, "2.5", ts.A2A.Version)
}
