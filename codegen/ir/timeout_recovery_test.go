package ir_test

// The generated child route, not a caller's name or tags, determines whether
// an exported timeout-recovery promise reaches every consumer unchanged.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestTimeoutRecoveryRequiresGeneratedAgentRoutes(t *testing.T) {
	const consumerOverride = "consumer override"
	for _, route := range []string{"agent", "inline", "service", "publish", "MCP", "registry", consumerOverride} {
		t.Run(route, func(t *testing.T) {
			genpkg, roots := testhelpers.RunDesign(t, func() {
				API("test", func() {})
				registry := Registry("registry", func() {
					URL("https://registry.example.com")
				})
				toolDSL := func() {
					Tool("find", "Find records", func() {
						if route != consumerOverride {
							ReplanOnTimeout()
						}
					})
				}
				args := []any{"lookup", toolDSL}
				switch route {
				case "registry":
					args = []any{"lookup", FromRegistry(registry, "lookup"), toolDSL}
				case "MCP":
					args = []any{"lookup", FromExternalMCP("provider", "lookup"), toolDSL}
				}
				lookup := Toolset(args...)
				Service("provider", func() {
					if route == "service" {
						Export(lookup)
						return
					}
					Agent("lookup", "Find records", func() {
						if route == "inline" || route == "MCP" || route == "registry" {
							Use(lookup)
							return
						}
						Export(lookup, func() {
							if route == "publish" {
								PublishTo(registry)
							}
						})
					})
				})
				if route == "agent" || route == consumerOverride {
					Service("consumer", func() {
						Agent("assistant", "Help with records", func() {
							Use(lookup, func() {
								if route == consumerOverride {
									Tool("find", "Find records", func() {
										ReplanOnTimeout()
									})
								}
							})
						})
					})
				}
			})
			design, err := buildDesign(t, genpkg, roots)
			if route == "agent" {
				require.NoError(t, err)
				assert.True(t, design.Toolsets[0].Expr.Tools[0].ReplanOnTimeout)
				return
			}
			require.ErrorContains(t, err, "ReplanOnTimeout")
		})
	}
}
