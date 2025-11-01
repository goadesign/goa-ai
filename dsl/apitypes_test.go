package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
	goaexpr "goa.design/goa/v3/expr"
)

func TestAPITypesAvailable(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("test", func() {
			Method("run", func() {
				Payload(AgentRunPayload)
				StreamingResult(AgentRunChunk)
			})
		})
	})

	require.Len(t, goaexpr.Root.Services, 1)
	svc := goaexpr.Root.Services[0]
	require.Len(t, svc.Methods, 1)
	method := svc.Methods[0]
	require.NotNil(t, method.Payload)
	require.NotNil(t, method.StreamingResult)
}

func TestAPITypesReferenceEachOther(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("test", func() {
			Method("test", func() {
				Payload(func() {
					Attribute("message", AgentMessage)
				})
				Result(func() {
					Attribute("event", AgentToolEvent)
					Attribute("annotation", AgentPlannerAnnotation)
				})
			})
		})
	})

	require.Len(t, goaexpr.Root.Services, 1)
	svc := goaexpr.Root.Services[0]
	require.Len(t, svc.Methods, 1)
	method := svc.Methods[0]
	require.NotNil(t, method.Payload)
	require.NotNil(t, method.Result)
}

