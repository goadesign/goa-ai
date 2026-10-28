package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
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

func TestResolveConsumerFieldBindings(t *testing.T) {
	scopeAttr := &expr.AttributeExpr{Type: expr.String}
	readInput := &expr.AttributeExpr{Type: &expr.UserTypeExpr{TypeName: "ReadInput", AttributeExpr: &expr.AttributeExpr{
		Type:       &expr.Object{{Name: "scope", Attribute: scopeAttr}, {Name: "query", Attribute: &expr.AttributeExpr{Type: expr.String}}},
		Validation: &expr.ValidationExpr{Required: []string{"scope", "query"}},
	}}}
	optionalInput := &expr.AttributeExpr{Type: &expr.UserTypeExpr{TypeName: "OptionalInput", AttributeExpr: &expr.AttributeExpr{
		Type: &expr.Object{{Name: "scope", Attribute: scopeAttr}, {Name: "cursor", Attribute: &expr.AttributeExpr{Type: expr.String}}},
	}}}
	otherInput := &expr.AttributeExpr{Type: &expr.UserTypeExpr{TypeName: "OtherInput", AttributeExpr: &expr.AttributeExpr{
		Type: &expr.Object{{Name: "query", Attribute: &expr.AttributeExpr{Type: expr.String}}},
	}}}

	ts := &agentexpr.ToolsetExpr{Name: "records"}
	readTool := &agentexpr.ToolExpr{
		Name:    "read",
		Toolset: ts,
		Args:    readInput,
		Bounds: &agentexpr.ToolBoundsExpr{
			Paging: &agentexpr.ToolPagingExpr{
				CursorField:  "cursor",
				NextCursorField: "next_cursor",
				ContinueTool: "continue_read",
			},
		},
	}
	continueReadTool := &agentexpr.ToolExpr{
		Name:    "continue_read",
		Toolset: ts,
		Args:    optionalInput,
		Bounds: &agentexpr.ToolBoundsExpr{
			Paging: &agentexpr.ToolPagingExpr{
				CursorField:     "cursor",
				NextCursorField: "next_cursor",
			},
		},
	}
	optionalTool := &agentexpr.ToolExpr{
		Name:    "search_optional",
		Toolset: ts,
		Args:    optionalInput,
	}
	otherTool := &agentexpr.ToolExpr{
		Name:    "list_all",
		Toolset: ts,
		Args:    otherInput,
	}
	ts.Tools = []*agentexpr.ToolExpr{readTool, continueReadTool, optionalTool, otherTool}

	plan := &consumerInputPlan{}
	lookupAttr := &expr.AttributeExpr{Type: expr.String, Description: "Scope name."}
	err := resolveConsumerFieldBindings(plan, ts.Tools, []*agentexpr.FieldBindingExpr{{
		Field:  "scope",
		Lookup: lookupAttr,
	}})
	require.NoError(t, err)
	require.Len(t, plan.tools, 2)
	assert.Equal(t, "read", plan.tools[0].expression.Name)
	assert.Equal(t, "search_optional", plan.tools[1].expression.Name)
	assert.Equal(t, lookupAttr, plan.tools[0].lookupAttr)
	assert.Equal(t, lookupAttr, plan.tools[1].lookupAttr)

	err = resolveConsumerFieldBindings(&consumerInputPlan{}, ts.Tools, []*agentexpr.FieldBindingExpr{{
		Field: "missing",
	}})
	require.ErrorContains(t, err, "matches no tools")
}

func TestConsumerToolsetFieldBindingHasUniformFieldType(t *testing.T) {
	stringLayout, err := codegen.PlanGoType(&expr.AttributeExpr{Type: expr.String, Description: "First description"}, codegen.GoTypePlanOptions{Owner: "test.local"})
	require.NoError(t, err)
	stringOtherDescLayout, err := codegen.PlanGoType(&expr.AttributeExpr{Type: expr.String, Description: "Second description"}, codegen.GoTypePlanOptions{Owner: "test.local"})
	require.NoError(t, err)
	intLayout, err := codegen.PlanGoType(&expr.AttributeExpr{Type: expr.Int}, codegen.GoTypePlanOptions{Owner: "test.local"})
	require.NoError(t, err)

	for _, tc := range []struct {
		name    string
		binding *consumerToolsetFieldBinding
		want    bool
	}{
		{
			name:    "empty tools",
			binding: &consumerToolsetFieldBinding{},
			want:    true,
		},
		{
			name: "single tool",
			binding: &consumerToolsetFieldBinding{
				tools: []*consumerInputTool{{
					fieldLayouts: []*codegen.GoTypePlan{stringLayout},
				}},
			},
			want: true,
		},
		{
			name: "multiple tools with same parameter type and different field descriptions",
			binding: &consumerToolsetFieldBinding{
				tools: []*consumerInputTool{
					{fieldLayouts: []*codegen.GoTypePlan{stringLayout}},
					{fieldLayouts: []*codegen.GoTypePlan{stringOtherDescLayout}},
				},
			},
			want: true,
		},
		{
			name: "multiple tools with non-equivalent layouts",
			binding: &consumerToolsetFieldBinding{
				tools: []*consumerInputTool{
					{fieldLayouts: []*codegen.GoTypePlan{stringLayout}},
					{fieldLayouts: []*codegen.GoTypePlan{intLayout}},
				},
			},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.binding.hasUniformFieldType())
		})
	}
}
