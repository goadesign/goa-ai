// Package testscenarios provides reusable Goa design scenarios for testing
// agent code generation.
package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// AliasBoth returns a design where tool payload and result reuse the exact
// method user types, enabling full adapter bypass.
func AliasBoth() func() {
	return func() {
		API("alpha", func() {})
		var PairPayload = Type("PairPayload", func() {
			Attribute("id", String, "ID")
			Required("id")
		})
		var PairResult = Type("PairResult", func() {
			Attribute("ok", Boolean, "OK")
			Required("ok")
		})
		Service("alpha", func() {
			Method("Pair", func() {
				Payload(PairPayload)
				Result(PairResult)
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("lookup", func() {
					aidsl.Tool("pair", "Pair", func() {
						aidsl.Args(PairPayload)
						aidsl.Return(PairResult)
						aidsl.BindTo("alpha", "Pair")
					})
				})
			})
		})
	}
}

// AliasPayloadOnly returns a design where payload reuses the method payload
// user type but result is inline, requiring a result adapter.
func AliasPayloadOnly() func() {
	return func() {
		API("alpha", func() {})
		var P = Type("PPayload", func() { Attribute("id", String); Required("id") })
		var PR = Type("PResult", func() { Attribute("ok", Boolean); Required("ok") })
		Service("alpha", func() {
			Method("Echo", func() {
				Payload(P)
				Result(PR)
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("echo", func() {
					aidsl.Tool("echo", "Echo", func() {
						aidsl.Args(P)
						aidsl.Return(PR)
						aidsl.BindTo("alpha", "Echo")
					})
				})
			})
		})
	}
}

// AliasResultOnly returns a design where result reuses the method result
// user type but payload is inline, requiring typed payload mapping.
func AliasResultOnly() func() {
	return func() {
		API("alpha", func() {})
		var R = Type("RResult", func() { Attribute("ok", Boolean); Required("ok") })
		var P = Type("PPayload", func() { Attribute("id", String); Required("id") })
		Service("alpha", func() {
			Method("Reply", func() {
				Payload(P)
				Result(R)
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("reply", func() {
					aidsl.Tool("reply", "Reply", func() {
						aidsl.Args(P)
						aidsl.Return(R)
						aidsl.BindTo("alpha", "Reply")
					})
				})
			})
		})
	}
}
