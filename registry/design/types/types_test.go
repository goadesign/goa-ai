package types_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"

	registrytypes "goa.design/goa-ai/registry/design/types"
)

func TestTypesComposeWithoutRegisteringService(t *testing.T) {
	require.Nil(t, expr.Root.API)
	require.Empty(t, expr.Root.Services)

	domainTypes := make([]expr.UserType, 0, 8)
	for _, name := range []string{"InputContinuation", "TaskAnswers", "ExecutionContinuation", "HostRequest", "PendingInput", "TaskWait", "TaskInput", "PendingExecution"} {
		domainType := dsl.Type(name, func() {
			dsl.Field(1, "value", dsl.String, "Application-owned value.")
			dsl.Required("value")
		})
		require.NotNil(t, domainType, "the imported framework schema must leave %s available to the application", name)
		domainTypes = append(domainTypes, domainType)
	}
	dsl.API("catalog", func() {})
	dsl.Service("catalog", func() {
		for _, domainType := range domainTypes {
			dsl.Method("domain_"+domainType.Name(), func() {
				dsl.Payload(domainType)
				dsl.GRPC(func() {})
			})
		}
		dsl.Method("register", func() {
			dsl.Payload(registrytypes.AgentToolsetDeclaration)
			dsl.GRPC(func() {})
		})
		dsl.Method("report_output", func() {
			dsl.Payload(registrytypes.PublishToolOutputDeltaPayload)
			dsl.GRPC(func() {})
		})
		dsl.Method("get_call_context", func() {
			dsl.Result(registrytypes.ToolCallMeta)
			dsl.GRPC(func() {})
		})
	})
	require.NoError(t, eval.RunDSL())
	require.Len(t, expr.Root.Services, 1)
	assert.Equal(t, "catalog", expr.Root.Services[0].Name)
	assert.Equal(t, "catalog", expr.Root.API.Name)
}
