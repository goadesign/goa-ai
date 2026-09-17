// These tests keep deferral on a consuming Use expression, never on a shared
// definition or export that would change other agents' loading choices.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
)

func TestDeferredBelongsToConsumer(t *testing.T) {
	runDSL(t, func() {
		shared := Toolset("shared", func() {
			Tool("lookup", "Look up a record.", func() {
				Args(func() {
					Attribute("query", String, "Record to find.")
					Required("query")
				})
				Return(String)
			})
		})
		Service("records", func() {
			Agent("deferred", "Discover tools.", func() {
				Use(shared, func() { Deferred() })
			})
			Agent("immediate", "Use tools directly.", func() {
				Use(shared)
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 2)
	assert.True(t, agentsexpr.Root.Agents[0].Used.Toolsets[0].Deferred)
	assert.False(t, agentsexpr.Root.Agents[1].Used.Toolsets[0].Deferred)
	assert.False(t, agentsexpr.Root.Toolsets[0].Deferred)
}

func TestDeferredRejectsSharedDefinitionAndExport(t *testing.T) {
	tests := []struct {
		name   string
		design func()
	}{
		{"definition", func() { Toolset("shared", func() { Deferred() }) }},
		{"export", func() {
			Service("records", func() {
				Agent("provider", "Provides tools.", func() {
					Export("shared", func() { Deferred() })
				})
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runDSLWithError(t, test.design)
			require.ErrorContains(t, err, "Deferred must appear inside a Use DSL function")
		})
	}
}

func TestUseRegistryPreservesConsumerLoadingChoice(t *testing.T) {
	runDSL(t, func() {
		registry := Registry("company", func() { URL("https://registry.example.invalid") })
		Service("records", func() {
			Agent("lazy", "Search current registry tools.", func() {
				Use(registry, func() { Deferred() })
			})
			Agent("eager", "Advertise current registry tools.", func() {
				Use(registry)
			})
		})
	})
	require.Len(t, agentsexpr.Root.Agents, 2)
	lazy := agentsexpr.Root.Agents[0]
	eager := agentsexpr.Root.Agents[1]
	require.Len(t, lazy.Registries, 1)
	require.Len(t, eager.Registries, 1)
	assert.True(t, lazy.Registries[0].Deferred)
	assert.False(t, eager.Registries[0].Deferred)
	assert.Nil(t, lazy.Used, "a whole registry is not an invented toolset")
}

func TestRegistryVersionBelongsToConsumer(t *testing.T) {
	runDSL(t, func() {
		registry := Registry("company", func() { URL("https://registry.example.invalid") })
		records := Toolset(FromRegistry(registry, "records"))
		Service("records", func() {
			Agent("pinned", "Use a fixed tool contract.", func() {
				Use(records, func() { Version("1.2.3") })
			})
			Agent("current", "Use the current tool contract.", func() {
				Use(records)
			})
		})
	})
	require.Len(t, agentsexpr.Root.Agents, 2)
	pinned := agentsexpr.Root.Agents[0].Used.Toolsets[0].Provider
	current := agentsexpr.Root.Agents[1].Used.Toolsets[0].Provider
	assert.Equal(t, "1.2.3", pinned.Version)
	assert.Empty(t, current.Version)
	assert.Empty(t, agentsexpr.Root.Toolsets[0].Provider.Version)
	assert.Same(t, pinned.Registry, current.Registry)
}

func TestUseRegistryRejectsOverlappingConsumption(t *testing.T) {
	for _, whole := range []bool{false, true} {
		name := "duplicate"
		if whole {
			name = "named and whole"
		}
		t.Run(name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				registry := Registry("company", func() { URL("https://registry.example.invalid") })
				records := Toolset(FromRegistry(registry, "records"))
				Service("records", func() {
					Agent("reader", "Read records.", func() {
						Use(registry, func() { Deferred() })
						if whole {
							Use(records)
						} else {
							Use(registry)
						}
					})
				})
			})
			if whole {
				require.ErrorContains(t, err, "overlaps whole registry")
			} else {
				require.ErrorContains(t, err, "consumes registry \"company\" more than once")
			}
		})
	}
}

func TestRegistryConsumptionRejectsConsumerAuthoredContracts(t *testing.T) {
	for _, test := range []struct {
		name string
		use  func(*agentsexpr.ToolsetExpr)
		want string
	}{
		{"inline", func(toolset *agentsexpr.ToolsetExpr) {
			Agent("consumer", "Consume tools.", func() {
				Use(toolset, func() { Tool("search", "Search records.") })
			})
		}, "cannot declare inline Tool schemas or subsets"},
		{"tags", func(toolset *agentsexpr.ToolsetExpr) {
			Agent("consumer", "Consume tools.", func() {
				Use(toolset, func() { Tags("consumer-tag") })
			})
		}, "tool tags belong to the provider"},
		{"publish", func(toolset *agentsexpr.ToolsetExpr) {
			Agent("consumer", "Consume tools.", func() {
				Use(toolset, func() { PublishTo(toolset.Provider.Registry) })
			})
		}, "cannot publish provider contracts"},
		{"agent export", func(toolset *agentsexpr.ToolsetExpr) {
			Agent("provider", "Export tools.", func() { Export(toolset) })
		}, "cannot be exported"},
		{"service export", func(toolset *agentsexpr.ToolsetExpr) { Export(toolset) }, "cannot be exported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				registry := Registry("company", func() { URL("https://registry.example.invalid") })
				records := Toolset(FromRegistry(registry, "records"))
				Service("records", func() { test.use(records) })
			})
			require.ErrorContains(t, err, test.want)
		})
	}
}
