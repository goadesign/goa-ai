package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// RunPolicyBasic returns a DSL design with caps and a time budget.
func RunPolicyBasic() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				RunPolicy(func() {
					DefaultCaps(MaxToolCalls(5), MaxRecoveryTurns(2))
					TimeBudget("30s")
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
