// Package design supplies synthetic tool contracts for text-only runtime tests.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("presentation-fixture", func() {})

var _ = Service("records", func() {
	Agent("reader", "Reads record counts.", func() {
		Use("records", func() {
			Tool("read", "Read a bounded record count.", func() {
				Args(func() {
					Attribute("query", String, "Record selection.")
					Attribute("renderUi", Boolean, "Show an interactive card.")
					Required("query")
				})
				UIOnly("renderUi")
				UIInstructions(" Display the card when render_ui=true.")
				Return(func() { Attribute("count", Int, "Number of records."); Required("count") })
				ServerData("record.card", func() {
					Attribute("count", Int, "Number shown in the card.")
					Required("count")
				}, func() { AudienceTimeline() })
			})
			Tool("show", "Show an interactive record chart.", func() {
				RequiresUI()
				Args(Empty)
				Return(Empty)
			})
			Tool("erase", "Erase selected records after confirmation.", func() {
				Args(Empty)
				Return(func() { Attribute("erased", Boolean, "Whether records were erased."); Required("erased") })
				Confirmation(func() {
					PromptTemplate("Erase the records?")
					DeniedResultTemplate(`{"erased":false}`)
				})
			})
		})
	})
})
