package types_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	_ "goa.design/goa-ai/runtime/agent/run/design/types"
)

func TestSubjectDesignDoesNotRegisterService(t *testing.T) {
	assert.Nil(t, expr.Root.API)
	assert.Empty(t, expr.Root.Services)
}

func TestRunSubjectRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name    string
		subject genrunidentity.RunSubject
		json    string
	}{
		{
			name: "agent",
			subject: genrunidentity.RunSubject{
				Executor: genrunidentity.NewRunExecutorAgent("assistant"),
			},
			json: `{"executor":{"type":"agent","value":"assistant"}}`,
		},
		{
			name: "service tool",
			subject: genrunidentity.RunSubject{
				Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{
					Registry: "primary",
					Tool:     "records.lookup",
				}),
			},
			json: `{"executor":{"type":"service_tool","value":{"registry":"primary","tool":"records.lookup"}}}`,
		},
		{
			name: "same tool from another registry",
			subject: genrunidentity.RunSubject{
				Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{
					Registry: "secondary",
					Tool:     "records.lookup",
				}),
			},
			json: `{"executor":{"type":"service_tool","value":{"registry":"secondary","tool":"records.lookup"}}}`,
		},
		{
			name: "qualified toolset",
			subject: genrunidentity.RunSubject{
				Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{
					Registry: "primary",
					Tool:     "records.archive.lookup",
				}),
			},
			json: `{"executor":{"type":"service_tool","value":{"registry":"primary","tool":"records.archive.lookup"}}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := genrunidentity.EncodeRunSubject(&test.subject)
			require.NoError(t, err)
			assert.JSONEq(t, test.json, string(encoded))
			decoded, err := genrunidentity.DecodeRunSubject(encoded)
			require.NoError(t, err)
			assert.Equal(t, test.subject, *decoded)
		})
	}
}

func TestRunSubjectRejectsInvalidJSON(t *testing.T) {
	for _, test := range []struct {
		name string
		json string
	}{
		{"missing executor", `{}`},
		{"null executor", `{"executor":null}`},
		{"empty executor", `{"executor":{}}`},
		{"unknown executor", `{"executor":{"type":"unknown","value":"assistant"}}`},
		{"empty agent", `{"executor":{"type":"agent","value":""}}`},
		{"combined branches", `{"executor":{"type":"agent","value":"assistant","service_tool":{"registry":"primary","tool":"records.lookup"}}}`},
		{"duplicate executor", `{"executor":{"type":"agent","value":"assistant"},"executor":{"type":"service_tool","value":{"registry":"primary","tool":"records.lookup"}}}`},
		{"null service tool", `{"executor":{"type":"service_tool","value":null}}`},
		{"missing registry", `{"executor":{"type":"service_tool","value":{"tool":"records.lookup"}}}`},
		{"empty registry", `{"executor":{"type":"service_tool","value":{"registry":"","tool":"records.lookup"}}}`},
		{"missing tool", `{"executor":{"type":"service_tool","value":{"registry":"primary"}}}`},
		{"unqualified tool", `{"executor":{"type":"service_tool","value":{"registry":"primary","tool":"lookup"}}}`},
		{"empty toolset", `{"executor":{"type":"service_tool","value":{"registry":"primary","tool":".lookup"}}}`},
		{"empty tool name", `{"executor":{"type":"service_tool","value":{"registry":"primary","tool":"records."}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := genrunidentity.DecodeRunSubject([]byte(test.json))
			assert.Error(t, err)
		})
	}
}

func TestRunSubjectRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name    string
		subject genrunidentity.RunSubject
	}{
		{"zero subject", genrunidentity.RunSubject{}},
		{"empty agent", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("")}},
		{"nil service tool", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(nil)}},
		{"missing registry", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Tool: "records.lookup"})}},
		{"unqualified tool", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "lookup"})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := genrunidentity.EncodeRunSubject(&test.subject)
			assert.Error(t, err)
		})
	}
}
