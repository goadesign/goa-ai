package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/expr"
)

func TestResolveConsumerInputFields(t *testing.T) {
	for _, test := range []struct {
		name                   string
		tool                   string
		fields                 []string
		required, injected, ui []string
		want                   string
	}{
		{name: "required", tool: "read", fields: []string{"scope"}, required: []string{"scope"}},
		{name: "unknown operation", tool: "missing", fields: []string{"scope"}, want: "unknown tool"},
		{name: "unknown field", tool: "read", fields: []string{"missing"}, want: "has no field"},
		{name: "optional", tool: "read", fields: []string{"scope"}, want: "must be required"},
		{name: "repeated", tool: "read", fields: []string{"scope", "scope"}, required: []string{"scope"}, want: "repeated"},
		{name: "injected", tool: "read", fields: []string{"scope"}, required: []string{"scope"}, injected: []string{"scope"}, want: "already supplied"},
		{name: "UI control", tool: "read", fields: []string{"scope"}, required: []string{"scope"}, ui: []string{"scope"}, want: "text-only policy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := &expr.AttributeExpr{Type: &expr.UserTypeExpr{TypeName: "ReadInput", AttributeExpr: &expr.AttributeExpr{
				Type:       &expr.Object{{Name: "scope", Attribute: &expr.AttributeExpr{Type: expr.String}}},
				Validation: &expr.ValidationExpr{Required: test.required},
			}}}
			tools := []*agentexpr.ToolExpr{{Name: "read", Args: input, InjectedFields: test.injected, UIOnlyFields: test.ui}}
			binding, err := resolveConsumerInput(tools, &agentexpr.InputBindingExpr{Tool: test.tool, Fields: test.fields})
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.fields, binding.fields)
			assert.True(t, input.IsRequired("scope"), "binding must not change provider required fields")
		})
	}
}

func TestRequiredStaticInputBindingsUsesCompiledIdentities(t *testing.T) {
	for _, test := range []struct {
		name       string
		registry   bool
		agentTools string
		tools      []*ToolData
		want       string
	}{
		{name: "local", tools: []*ToolData{{Name: "read", QualifiedName: "compiled.owner.read"}}},
		{name: "agent tool", agentTools: "generated.local/gen/worker/agenttools/records", tools: []*ToolData{{Name: "read", QualifiedName: "compiled.owner.read"}}},
		{name: "missing operation", want: "no compiled declaration"},
		{name: "registry", registry: true, tools: []*ToolData{{Name: "read", QualifiedName: "compiled.owner.read"}}, want: "registry toolset"},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := &AgentData{ID: "reader.bound", UsedToolsets: []*ToolsetData{{
				Name:                 "consumer-route",
				IsRegistryBacked:     test.registry,
				AgentToolsImportPath: test.agentTools,
				Expr:                 &agentexpr.ToolsetExpr{InputBindings: []*agentexpr.InputBindingExpr{{Tool: "read", Fields: []string{"scope"}}}},
				Tools:                test.tools,
			}}}
			names, err := requiredStaticInputBindings(agent)
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{"compiled.owner.read"}, names)
		})
	}
}
