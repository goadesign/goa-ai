package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goaexpr "goa.design/goa/v3/expr"
)

// Test that Prepare recursively marks user types referenced by tool args/return
// with type:generate:force and ensures they are present in goa Root.Types.
func TestPrepare_ForceGenerateToolTypesRecursively(t *testing.T) {
	// Reset global roots used by the plugin.
	agentsExpr.Root = &agentsExpr.RootExpr{}
	goaexpr.Root.Types = nil

	// Build nested user types: A{ B *B }, B{ C string }.
	bObj := &goaexpr.Object{&goaexpr.NamedAttributeExpr{Name: "c", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}}}
	utB := &goaexpr.UserTypeExpr{AttributeExpr: &goaexpr.AttributeExpr{Type: bObj}, TypeName: "B"}
	aObj := &goaexpr.Object{&goaexpr.NamedAttributeExpr{Name: "b", Attribute: &goaexpr.AttributeExpr{Type: utB}}}
	utA := &goaexpr.UserTypeExpr{AttributeExpr: &goaexpr.AttributeExpr{Type: aObj}, TypeName: "A"}

	// Create a minimal agent with one tool referencing A in args and B in return.
	svc := &goaexpr.ServiceExpr{Name: "svc"}
	ag := &agentsExpr.AgentExpr{Name: "agent", Service: svc, Used: &agentsExpr.ToolsetGroupExpr{}}
	ts := &agentsExpr.ToolsetExpr{Name: "ts", Agent: ag}
	tool := &agentsExpr.ToolExpr{Name: "t", Toolset: ts, Args: &goaexpr.AttributeExpr{Type: utA}, Return: &goaexpr.AttributeExpr{Type: utB}}
	ts.Tools = []*agentsExpr.ToolExpr{tool}
	ag.Used.Toolsets = []*agentsExpr.ToolsetExpr{ts}
	agentsExpr.Root.Agents = []*agentsExpr.AgentExpr{ag}

	// Sanity: types are not in root yet.
	require.Nil(t, goaexpr.Root.UserType("A"))
	require.Nil(t, goaexpr.Root.UserType("B"))

	// Run Prepare
	err := Prepare("example.com/mod", nil)
	require.NoError(t, err)

	// Both types must be force-generated and present in Root.Types.
	gotA := goaexpr.Root.UserType("A")
	gotB := goaexpr.Root.UserType("B")
	require.NotNil(t, gotA)
	require.NotNil(t, gotB)
	_, ok := gotA.Attribute().Meta["type:generate:force"]
	require.True(t, ok, "A must be marked type:generate:force")
	_, ok = gotB.Attribute().Meta["type:generate:force"]
	require.True(t, ok, "B must be marked type:generate:force")
}

func TestPrepare_ForceGenerateReferencedUserTypes(t *testing.T) {
	// Reset global roots used by the plugin.
	agentsExpr.Root = &agentsExpr.RootExpr{}
	goaexpr.Root.Types = nil

	// TaskStepStatus is a user type that is referenced via AttributeExpr.References
	// (common in Goa when using Field(..., SomeType)).
	taskStepStatus := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{
			Type: goaexpr.String,
		},
		TypeName: "TaskStepStatus",
	}

	progressObj := &goaexpr.Object{
		&goaexpr.NamedAttributeExpr{
			Name: "status",
			Attribute: &goaexpr.AttributeExpr{
				Type:       goaexpr.String,
				References: []goaexpr.DataType{taskStepStatus},
			},
		},
	}
	taskStepProgress := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{
			Type: progressObj,
		},
		TypeName: "TaskStepProgress",
	}

	// Create a minimal agent with one tool referencing TaskStepProgress.
	svc := &goaexpr.ServiceExpr{Name: "svc"}
	ag := &agentsExpr.AgentExpr{Name: "agent", Service: svc, Used: &agentsExpr.ToolsetGroupExpr{}}
	ts := &agentsExpr.ToolsetExpr{Name: "ts", Agent: ag}
	tool := &agentsExpr.ToolExpr{
		Name:    "t",
		Toolset: ts,
		Args:    &goaexpr.AttributeExpr{Type: taskStepProgress},
		Return:  &goaexpr.AttributeExpr{Type: goaexpr.Empty},
	}
	ts.Tools = []*agentsExpr.ToolExpr{tool}
	ag.Used.Toolsets = []*agentsExpr.ToolsetExpr{ts}
	agentsExpr.Root.Agents = []*agentsExpr.AgentExpr{ag}

	err := Prepare("example.com/mod", nil)
	require.NoError(t, err)

	got := goaexpr.Root.UserType("TaskStepStatus")
	require.NotNil(t, got)
	_, ok := got.Attribute().Meta["type:generate:force"]
	require.True(t, ok, "TaskStepStatus must be marked type:generate:force")
}

func TestPrepare_DoesNotSynthesizeUnionBranchTypes(t *testing.T) {
	// Reset global roots used by the plugin.
	agentsExpr.Root = &agentsExpr.RootExpr{}
	goaexpr.Root.Types = nil

	u := &goaexpr.Union{
		TypeName: "Value",
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "number_value", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Float64}},
			{Name: "boolean_value", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Boolean}},
			{Name: "enum_value", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
			{Name: "text_value", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		},
	}
	propertyValueObj := &goaexpr.Object{
		&goaexpr.NamedAttributeExpr{
			Name:      "value",
			Attribute: &goaexpr.AttributeExpr{Type: u},
		},
	}
	propertyValue := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{
			Type: propertyValueObj,
		},
		TypeName: "PropertyValue",
	}

	// Create a minimal agent with one tool referencing PropertyValue.
	svc := &goaexpr.ServiceExpr{Name: "svc"}
	ag := &agentsExpr.AgentExpr{Name: "agent", Service: svc, Used: &agentsExpr.ToolsetGroupExpr{}}
	ts := &agentsExpr.ToolsetExpr{Name: "ts", Agent: ag}
	tool := &agentsExpr.ToolExpr{
		Name:    "t",
		Toolset: ts,
		Args:    &goaexpr.AttributeExpr{Type: propertyValue},
		Return:  &goaexpr.AttributeExpr{Type: goaexpr.Empty},
	}
	ts.Tools = []*agentsExpr.ToolExpr{tool}
	ag.Used.Toolsets = []*agentsExpr.ToolsetExpr{ts}
	agentsExpr.Root.Agents = []*agentsExpr.AgentExpr{ag}

	err := Prepare("example.com/mod", nil)
	require.NoError(t, err)

	got := goaexpr.Root.UserType("PropertyValue")
	require.NotNil(t, got)
	_, ok := got.Attribute().Meta["type:generate:force"]
	require.True(t, ok, "PropertyValue must be marked type:generate:force")

	// Union branch types are generated by Goa when emitting the union type.
	// They must not be synthesized as user types in the expression tree as that
	// creates duplicate names and breaks cross-package references.
	for _, name := range []string{"ValueNumberValue", "ValueBooleanValue", "ValueEnumValue", "ValueTextValue"} {
		require.Nil(t, goaexpr.Root.UserType(name), "union branch user type %q must not be synthesized", name)
	}
}
