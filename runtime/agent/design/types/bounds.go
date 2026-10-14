// Package types exposes the runtime's shared result coverage value for service
// contracts. The tool's saved declaration determines when these fields apply;
// this value does not impose another query or operation limit.
package types

import (
	"goa.design/goa-ai/runtime/agent"
	. "goa.design/goa/v3/dsl"
)

// ResultBounds preserves the runtime's exact successful-result coverage data.
var ResultBounds = Type("ResultBounds", func() {
	Meta("struct:pkg:path", "toolbounds")
	CreateFrom(agent.Bounds{})
	ConvertTo(agent.Bounds{})
	Field(1, "returned", Int, "Number of items included in this result.")
	Field(2, "total", Int, "Provider-owned total, when known.")
	Field(3, "truncated", Boolean, "Whether the result was bounded before exhausting the selected data.")
	Field(4, "next_cursor", String, "Exact provider cursor for additional results, when present.")
	Field(5, "refinement_hint", String, "Provider guidance for refining the query.")
	Required("returned", "truncated")
})
