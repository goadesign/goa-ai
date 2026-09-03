// Package testscenarios defines small Goa designs used by generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// RecursiveRootArgs defines a tool whose named root input refers to itself.
func RecursiveRootArgs() func() {
	return func() {
		API("trees", func() {})
		node := Type("Node", func() {
			Attribute("name", String, "Node name.")
			Attribute("next", "Node", "Next node.")
			Required("name")
			Example(map[string]any{
				"name": "root",
				"next": map[string]any{"name": "leaf"},
			})
		})
		Service("trees", func() {
			Agent("walker", "Walk recursive trees", func() {
				Use("nodes", func() {
					Tool("walk", "Walk a tree", func() {
						Args(node)
					})
				})
			})
		})
	}
}
