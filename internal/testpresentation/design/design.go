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
					Attribute("renderSummary", Boolean, "Show an interactive summary.", func() { Default(false) })
					Required("query")
				})
				UIOnly("renderUi", "renderSummary")
				UIInstructions(" Display the card when render_ui=true.")
				ResultReminder("Report the count with its selected scope.")
				UIResultReminder("The user sees an interactive record card.")
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

var providerQueries = Toolset("provider_queries", func() {
	Tool("read", "Read a record count through a service.", func() {
		Return(func() {
			Attribute("count", Int, "Number of records.")
			Required("count")
		})
		BindTo("record_provider", "read")
		Inject("sessionId")
		UIOnly("renderUi", "renderSummary")
		ServerData("record.reference", String, func() {
			FromMethodResultField("reference")
			AudienceEvidence()
		})
	})
})

var _ = Service("record_provider", func() {
	Method("read", func() {
		Payload(func() {
			Attribute("query", String, "Record selection.")
			Attribute("renderUi", Boolean, "Show an interactive card.")
			Attribute("renderSummary", Boolean, "Show an interactive summary.", func() { Default(false) })
			Attribute("sessionId", String, "Session supplied by the caller metadata.")
			Attribute("pageToken", String, "Page selected by the execution caller.")
			Required("query", "sessionId")
		})
		Result(func() {
			Attribute("count", Int, "Number of records.")
			Attribute("reference", String, "Reference for the count.")
			Required("count", "reference")
		})
	})
	Export(providerQueries)
})
