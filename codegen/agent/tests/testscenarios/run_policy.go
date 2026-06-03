package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// RunPolicyBasic returns a DSL design with caps, time budget, and interrupts.
func RunPolicyBasic() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				RunPolicy(func() {
					DefaultCaps(MaxToolCalls(5), MaxConsecutiveFailedToolCalls(2))
					TimeBudget("30s")
					InterruptsAllowed(true)
				})
				Use("helpers", func() {
					Tool("noop", "Noop", func() {})
				})
			})
		})
	}
}

// RunPolicyHistoryCompressTokens returns a DSL design that exercises generated
// token-budget history compression wiring.
func RunPolicyHistoryCompressTokens() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				RunPolicy(func() {
					History(func() {
						CompressAtMaxInputTokens(120000)
						KeepMaxInputTokens(40000)
						KeepMaxTurns(12)
					})
				})
			})
		})
	}
}
