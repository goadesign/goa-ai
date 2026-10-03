package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	agentexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
)

func TestResultRemindersRetainDeclaredAudience(t *testing.T) {
	runDSL(t, func() {
		API("records", func() {})
		Service("records", func() {
			Agent("reader", "Reads counts.", func() {
				Use("records", func() {
					Tool("read", "Read a count.", func() {
						Args(Empty)
						Return(Empty)
						ResultReminder("Report the selected scope.")
						UIResultReminder("The user sees a chart.")
					})
				})
			})
		})
	})
	tool := agentexpr.Root.Agents[0].Used.Toolsets[0].Tools[0]
	assert.Equal(t, "Report the selected scope.", tool.ResultReminder)
	assert.Equal(t, "The user sees a chart.", tool.UIResultReminder)
}
