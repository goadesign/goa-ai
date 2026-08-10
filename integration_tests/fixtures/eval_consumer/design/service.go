package design

import . "goa.design/goa/v3/dsl"

var _ = Service("status", func() {
	Description("Reports whether the eval consumer fixture is available.")
	Method("get", func() {
		Description("Returns the fixture status for generated-consumer tests.")
		Result(String)
	})
})
