// Package design describes the Choice subset of TypeSafe's published System One
// API: https://docs.typesafe.ai/api. Generated codecs own JSON field presence,
// exact shapes, numeric ranges, and decoding. The adapter owns matching question
// IDs, probability sums, and the pinned response model.
package design

import (
	_ "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("typesafe_choice", func() {
	Description("Typed records for native TypeSafe Choice requests and responses.")
})

var ModelVersion = Type("ModelVersion", String, func() {
	Description("An immutable Jev release, such as jev-1.13.0; mutable aliases cannot qualify decisions.")
	Pattern(`^jev-[0-9]+\.[0-9]+\.[0-9]+$`)
})

var State = Type("State", func() {
	Attribute("subject", String, "Captured content being assessed.")
	Attribute("reference", String, "Captured facts; these cannot supply missing subject content.")
	Required("subject", "reference")
})

var Instructions = Type("Instructions", func() {
	Attribute("contract", String, "The common grading contract.")
	Attribute("requirement", String, "The fixed semantic requirement.")
	Required("contract", "requirement")
})

var Criteria = Type("Criteria", func() {
	Attribute("a", String, "Meaning of the first option.")
	Attribute("b", String, "Meaning of the second option.")
	Attribute("c", String, "Meaning of the third option.")
	Attribute("d", String, "Meaning of the fourth option.")
	Required("a", "b", "c", "d")
})

var Question = Type("Question", func() {
	Attribute("type", String, "Native primitive used for this question.", func() { Enum("choice") })
	Attribute("instructions", Instructions, "The exact grading contract and requirement.")
	Attribute("criteria", Criteria, "Four option meanings in the configured order.")
	Required("type", "instructions", "criteria")
})

var Request = Type("Request", func() {
	Attribute("state", State, "Evidence shared by all questions in this request.")
	Attribute("model", ModelVersion, "Pinned Jev model version.")
	Attribute("questions", MapOf(String, Question), "Requirements keyed by caller-owned identities.", func() { MinLength(1) })
	Required("state", "model", "questions")
})

var Probabilities = Type("Probabilities", func() {
	for _, option := range []string{"a", "b", "c", "d"} {
		Attribute(option, Float64, "Probability for option "+option+".", func() {
			Minimum(0)
			Maximum(1)
		})
	}
	Required("a", "b", "c", "d")
})

var Answer = Type("Answer", func() {
	Attribute("type", String, "Native primitive that produced this answer.", func() { Enum("choice") })
	Attribute("choice", String, "The highest-probability option.", func() { Enum("a", "b", "c", "d") })
	Attribute("probabilities", Probabilities, "Complete option probabilities, whose sum is checked by the adapter.")
	Attribute("confidence", Float64, "Provider confidence derived from the option distribution; routing uses P(entailed).", func() {
		Minimum(0)
		Maximum(1)
	})
	Required("type", "choice", "probabilities", "confidence")
})

var Usage = Type("Usage", func() {
	Attribute("input_tokens", Int, "Reported input tokens, when supplied.", func() { Minimum(0) })
	Attribute("output_tokens", Int, "Reported output tokens, when supplied.", func() { Minimum(0) })
})

var ChoiceResponse = Type("Response", func() {
	Attribute("model", String, "Actual model version used for inference.")
	Attribute("answers", MapOf(String, Answer), "Answers under the exact requested identities.")
	Attribute("usage", Usage, "Reported token usage; absent counts remain unknown.")
	Required("model", "answers", "usage")
})

var _ = Service("typesafe", func() {
	Description("Defines native Choice records for assessing captured evidence with a pinned System One model. The adapter sends these records to TypeSafe and validates request-to-response correspondence.")
	Method("classify", func() {
		Description("Classifies fixed requirements sharing captured content and factual context, returning complete probabilities for each requirement and the model that produced them.")
		Payload(Request)
		Result(ChoiceResponse)
	})
})
