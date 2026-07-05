package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ModelJSONNames returns a DSL with lowerCamel Goa attributes that should be
// projected to snake_case in model-facing tool JSON.
func ModelJSONNames() func() {
	return func() {
		API("alpha", func() {})

		var TimeContext = Type("TimeContext", func() {
			Field(1, "startTime", String, "Start time for the request.")
			Field(2, "endTime", String, "End time for the request.")
			Required("startTime", "endTime")
		})

		var InspectPayload = Type("InspectPayload", func() {
			Field(1, "deviceAlias", String, "Device alias to inspect.")
			Field(2, "renderUi", Boolean, "Whether the tool should render UI output.")
			Field(3, "sourceIds", ArrayOf(String), "Optional source identifiers.")
			Field(4, "timeContext", TimeContext, "Time window for the inspection.")
			Required("deviceAlias", "renderUi", "timeContext")
			Example(Val{
				"deviceAlias": "ahu_1",
				"renderUi":    true,
				"sourceIds":   []string{"temp", "pressure"},
				"timeContext": Val{
					"startTime": "2026-01-01T00:00:00Z",
					"endTime":   "2026-01-01T01:00:00Z",
				},
			})
		})

		var OperatorSummary = Type("OperatorSummary", func() {
			Field(1, "userId", String, "Operator user identifier.")
			Field(2, "firstName", String, "Operator first name.")
			Field(3, "lastName", String, "Operator last name.")
			Required("userId", "firstName", "lastName")
		})

		var InspectResult = Type("InspectResult", func() {
			Field(1, "resultSummary", String, "Inspection summary.")
			Field(2, "operatorSummaries", ArrayOf(OperatorSummary), "Operators related to the inspection.", func() {
				Example([]Val{
					{
						"userId":    "operator_1",
						"firstName": "Ada",
						"lastName":  "Lovelace",
					},
				})
			})
			Required("resultSummary", "operatorSummaries")
		})

		Service("alpha", func() {
			Agent("scribe", "Inspection helper", func() {
				Use("inspect", func() {
					Tool("inspect_device", "Inspect a device.", func() {
						Args(InspectPayload)
						Return(InspectResult)
					})
				})
			})
		})
	}
}
