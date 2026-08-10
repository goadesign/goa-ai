// Package design defines a small evaluation suite used to prove that an
// external Goa application can generate and consume Goa-AI eval code.
package design

import . "goa.design/goa-ai/eval/dsl"

var _ = Suite("chat_quality", func() {
	Description("Checks two independent chat outcomes.")
	Timeout("5s")

	Scenario("alarm_inventory", func() {
		Description("Checks an alarm inventory answer.")
		Input("List every alarm.")
		Tags("smoke")
	})

	Scenario("equipment_status", func() {
		Description("Checks an equipment status answer.")
		Input("Summarize equipment status.")
		Tags("smoke", "equipment")
	})
})
