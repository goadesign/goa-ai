package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// RunPolicyBasic returns a DSL design with caps and a time budget.
func RunPolicyBasic() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.RunPolicy(func() {
					aidsl.DefaultCaps(aidsl.MaxToolCalls(5), aidsl.MaxConsecutiveFailedToolCalls(2))
					aidsl.TimeBudget("30s")
				})
				aidsl.Use("helpers", func() {
					aidsl.Tool("noop", "Noop", func() {})
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
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.RunPolicy(func() {
					aidsl.History(func() {
						aidsl.CompressAtMaxInputTokens(120000)
						aidsl.KeepMaxInputTokens(40000)
						aidsl.KeepMaxTurns(12)
					})
				})
			})
		})
	}
}
