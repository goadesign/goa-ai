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

		var ReviewPayload = Type("ReviewPayload", func() {
			Field(1, "recordKey", String, "Record key to review.")
			Field(2, "includeDetails", Boolean, "Whether the tool should include detailed output.")
			Field(3, "sourceIds", ArrayOf(String), "Optional source identifiers.")
			Field(4, "timeContext", TimeContext, "Time window for the review.")
			Required("recordKey", "includeDetails", "timeContext")
			Example(Val{
				"recordKey":      "record_1",
				"includeDetails": true,
				"sourceIds":      []string{"source_1", "source_2"},
				"timeContext": Val{
					"startTime": "2026-01-01T00:00:00Z",
					"endTime":   "2026-01-01T01:00:00Z",
				},
			})
		})

		var ReviewerSummary = Type("ReviewerSummary", func() {
			Field(1, "userId", String, "Reviewer identifier.")
			Field(2, "firstName", String, "Reviewer first name.")
			Field(3, "lastName", String, "Reviewer last name.")
			Required("userId", "firstName", "lastName")
		})

		var ReviewResult = Type("ReviewResult", func() {
			Field(1, "summaryText", String, "Review summary.")
			Field(2, "reviewerSummaries", ArrayOf(ReviewerSummary), "Reviewers associated with the result.", func() {
				Example([]Val{
					{
						"userId":    "reviewer_1",
						"firstName": "Example",
						"lastName":  "Reviewer",
					},
				})
			})
			Required("summaryText", "reviewerSummaries")
		})

		Service("alpha", func() {
			Agent("scribe", "Record review helper", func() {
				Use("review", func() {
					Tool("review_record", "Review a record.", func() {
						Args(ReviewPayload)
						Return(ReviewResult)
					})
				})
			})
		})
	}
}
