package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceCompletionPrimitive returns a DSL with a completion that produces a
// primitive result so codec generation cannot assume transport helper types.
func ServiceCompletionPrimitive() func() {
	return func() {
		API("tasks", func() {})

		Service("tasks", func() {
			aidsl.Completion("headline", "Write a short task headline", func() {
				aidsl.Return(String)
			})
		})
	}
}

// ServiceCompletionLocatedCollection returns a direct collection completion
// whose element type belongs to a Goa package outside the completion package.
func ServiceCompletionLocatedCollection() func() {
	return func() {
		API("tasks", func() {})

		var CompletionItem = Type("CompletionItem", func() {
			Attribute("value", String, "Item value")
			Required("value")
			Meta("struct:pkg:path", "types")
		})

		Service("tasks", func() {
			aidsl.Completion("items", "Return task items", func() {
				aidsl.Return(ArrayOf(CompletionItem))
			})
		})
	}
}

// ServiceCompletionLocatedResultBranch returns a completion whose union uses
// an authored Goa result type from an external package and a primitive branch.
func ServiceCompletionLocatedResultBranch() func() {
	return func() {
		API("tasks", func() {})

		var LocatedResult = ResultType("application/vnd.tasks.located-result", "LocatedResult", func() {
			Attribute("value", String, "Result value")
			Required("value")
			Meta("struct:pkg:path", "types")
		})

		var CompletionChoice = Type("CompletionChoice", func() {
			OneOf("selection", func() {
				Attribute("located", LocatedResult, "Located result")
				Attribute("message", String, "Plain message")
			})
			Required("selection")
		})

		Service("tasks", func() {
			aidsl.Completion("choice", "Return one task choice", func() {
				aidsl.Return(CompletionChoice)
			})
		})
	}
}
