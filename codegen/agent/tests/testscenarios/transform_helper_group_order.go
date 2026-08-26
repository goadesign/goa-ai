// Package testscenarios defines a generated-code scenario where one nested
// event helper is reused across three service result layouts.
package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TransformHelperGroupOrder puts one shared event below a key event and
// returns the shared event directly from two other methods.
func TransformHelperGroupOrder() func() {
	return func() {
		API("transform helper group order", func() {})

		var RuntimeChange = Type("RuntimeChange", func() {
			Attribute("application", String, "Application changed by the event.")
			Required("application")
		})
		var KeyEvent = Type("KeyEvent", func() {
			Attribute("key", String, "Key that changed.")
			Attribute("runtime_change", RuntimeChange, "Optional runtime change recorded with the key.")
			Required("key")
		})
		var KeyEventsResult = Type("KeyEventsResult", func() {
			Attribute("events", ArrayOf(KeyEvent), "Key events returned to the caller.", func() {
				MinLength(1)
			})
			Required("events")
		})
		var RuntimeChangesResult = Type("RuntimeChangesResult", func() {
			Attribute("events", ArrayOf(RuntimeChange), "Runtime changes returned to the caller.", func() {
				MinLength(1)
			})
			Required("events")
		})

		Service("records", func() {
			Method("GetKeyEvents", func() {
				Result(KeyEventsResult)
			})
			Method("ListRuntimeChanges", func() {
				Result(RuntimeChangesResult)
			})
			Method("WatchRuntimeChanges", func() {
				Result(RuntimeChangesResult)
			})
			aidsl.Agent("reader", "Reads generated record values.", func() {
				aidsl.Use("records", func() {
					aidsl.Tool("get_key_events", "Returns key events with optional runtime changes.", func() {
						aidsl.BindTo("GetKeyEvents")
					})
					aidsl.Tool("list_runtime_changes", "Returns runtime changes directly.", func() {
						aidsl.BindTo("ListRuntimeChanges")
					})
					aidsl.Tool("watch_runtime_changes", "Returns another set of runtime changes directly.", func() {
						aidsl.BindTo("WatchRuntimeChanges")
					})
				})
			})
		})
	}
}
