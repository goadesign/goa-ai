// Package testscenarios declares a native service whose host answers remain
// separate from the arguments and completed result shown to the model.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// NativeInputExchange binds one typed input exchange through local and registry tools.
func NativeInputExchange() func() {
	return nativeInputExchange(false)
}

// NativeInputExchangeView retains the same host contract under a service-selected HTTP view.
func NativeInputExchangeView() func() {
	return nativeInputExchange(true)
}

// nativeInputExchange selects the result representation before the DSL evaluates.
func nativeInputExchange(viewed bool) func() {
	return func() {
		API("records", func() {})
		empty := Type("EmptyAnswer", func() {})
		content := Type("HostContent", func() {
			Field(1, "label", String, "Label selected by the host", func() { MinLength(1) })
			Field(2, "quantity", Int, "Optional whole count selected by the host")
			Required("label")
		})
		accepted := Type("Accepted", func() { Field(1, "content", content, "Accepted values"); Required("content") })
		answer := Type("Answer", func() {
			OneOf("answer", "Host decision", func() {
				TypeName("HostAnswer")
				Attribute("accept", accepted, "Accepted form")
				Attribute("decline", empty, "Declined form")
				Attribute("cancel", empty, "Cancelled form")
			})
			Required("answer")
		})
		urlAnswer := Type("URLAnswer", func() {
			OneOf("answer", "URL consent decision", func() {
				TypeName("URLDecision")
				Attribute("accept", empty, "Accepted URL interaction")
				Attribute("decline", empty, "Declined interaction")
				Attribute("cancel", empty, "Cancelled interaction")
			})
			Required("answer")
		})
		responses := Type("Responses", func() {
			Field(1, "profile", answer, "Profile answer")
			Field(2, "payment", urlAnswer, "External consent answer")
		})
		continuation := Type("Continuation", func() {
			Field(1, "state", String, "Exact returned state", func() { Meta("struct:field:name", "OpaqueState") })
			Field(2, "responses", responses, "Host answers")
		})
		question := Type("Question", func() {
			Field(1, "message", String, "Question shown to the host", func() { MinLength(1); Meta("struct:field:name", "PromptText") })
			Required("message")
		})
		urlQuestion := Type("URLQuestion", func() {
			Field(1, "message", String, "Consent requested from the host")
			Field(2, "url", String, "External consent URL", func() { Format(FormatURI) })
			Required("message", "url")
		})
		requests := Type("Requests", func() {
			Field(1, "profile", question, "Selected profile question")
			Field(2, "payment", urlQuestion, "External consent request")
		})
		pending := Type("Pending", func() {
			Field(1, "state", String, "Opaque invocation state")
			Field(2, "requests", requests, "Selected host questions")
		})
		evidence := Type("Evidence", func() { Field(1, "note", String, "Evidence retained by the server"); Required("note") })
		complete := Type("Completed", func() {
			Field(1, "label", String, "Completed label")
			Field(2, "returned", Int, "Number of labels returned")
			Field(3, "truncated", Boolean, "Whether additional labels are available")
			Field(4, "evidence", evidence, "Evidence produced with this value")
			Field(5, "refinement_hint", String, "How to narrow a truncated result")
			Required("label", "returned", "truncated")
		})
		publicResult := Type("PublicResult", func() { Field(1, "label", String, "Completed label"); Required("label") })
		resultDSL := func() {
			OneOf("outcome", "Completed value or required host input", func() {
				TypeName("OperationOutcome")
				Attribute("complete", complete, "Completed label")
				Attribute("input_required", pending, "Requested host input")
			})
			Required("outcome")
			if viewed {
				View("default", func() { Attribute("outcome") })
				View("alternate", func() { Attribute("outcome") })
			}
		}
		var outcome any
		if viewed {
			outcome = ResultType("application/vnd.native.operation", func() { TypeName("Operation"); resultDSL() })
		} else {
			outcome = Type("Operation", resultDSL)
		}
		Service("records", func() {
			Description("Reads synthetic labels after collecting host input.")
			Method("read", func() {
				Description("Returns a label after the host completes the requested form.")
				Payload(func() {
					Field(1, "target", String, "Requested synthetic label")
					Field(2, "session_id", String, "Session supplied by the runtime")
					Field(3, "continuation", continuation, "Host-owned input round", func() { Meta("struct:field:name", "HostInput") })
					Required("target", "session_id")
				})
				Result(outcome)
				InputExchange("continuation", "outcome")
			})
			Agent("scribe", "Reads synthetic labels.", func() {
				Use("lookup", func() {
					Tool("read", "Read a synthetic label", func() { BindTo("read"); Inject("session_id") })
					Tool("read_projected", "Read a label and retain its evidence", func() {
						BindTo("read")
						Inject("session_id")
						Return(publicResult)
						BoundedResult()
						ServerData("evidence", evidence, func() { FromMethodResultField("evidence"); AudienceEvidence() })
					})
				})
			})
		})
	}
}
