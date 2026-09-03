package codegen

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/ir"
	agentexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestPlanToolSpecsKeepsPublicAndHTTPUnionNamesIndependent(t *testing.T) {
	union := &goaexpr.Union{
		TypeName: "TargetSeries",
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "explicit", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
			{Name: "ranked", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Int}},
		},
	}
	toolsetExpr := &agentexpr.ToolsetExpr{
		Name: "analytics",
		Tools: []*agentexpr.ToolExpr{
			{
				Name:   "find",
				Args:   &goaexpr.AttributeExpr{Type: &goaexpr.Object{}},
				Return: &goaexpr.AttributeExpr{Type: union},
			},
		},
	}
	goaRoot := toolSpecsTestGoaRoot("union-names")
	generation, err := goacodegen.NewGeneration("generated.local/gen", []eval.Root{goaRoot})
	require.NoError(t, err)
	toolset := toolSpecsTestToolset(
		toolsetExpr,
		"analytics",
		"generated.local/gen/alpha/toolsets/analytics",
		"gen/alpha/toolsets/analytics",
	)
	planned, err := planToolSpecs(generation, &ir.Design{
		GoaRoot:  goaRoot,
		Toolsets: []*ir.Toolset{toolset},
	}, goaRoot.API, nil)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())

	packages, err := planned.packageFor("gen/alpha/toolsets/analytics")
	require.NoError(t, err)
	result := packages.tools["find"].resultType
	public, err := packages.public.Union(result.publicShape)
	require.NoError(t, err)
	transport, err := packages.transport.Union(result.transportShape)
	require.NoError(t, err)
	require.Equal(t, "TargetSeries", public.Declaration().Name())
	require.Equal(t, "TargetSeries", transport.Declaration().Name())
}

// TestPlanToolSpecsKeepsLargePackageNamesSeparate checks that a name used by
// the public package does not change the same name in the HTTP package.
func TestPlanToolSpecsKeepsLargePackageNamesSeparate(t *testing.T) {
	union := &goaexpr.Union{
		TypeName: "TargetSeries",
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "explicit", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
			{Name: "ranked", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Int}},
		},
	}
	unionAttribute := &goaexpr.AttributeExpr{Type: union}
	tools := make([]*agentexpr.ToolExpr, 0, 32)
	for index := range 32 {
		name := fmt.Sprintf("find_%02d", index)
		if index == 0 {
			name = "find"
		}
		tools = append(tools, &agentexpr.ToolExpr{
			Name: name,
			Args: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
				{
					Name:      "target",
					Attribute: unionAttribute,
				},
			}},
		})
	}
	const importPath = "generated.local/gen/alpha/toolsets/analytics"
	goaRoot := toolSpecsTestGoaRoot("large-package")
	generation, err := goacodegen.NewGeneration("generated.local/gen", []eval.Root{goaRoot})
	require.NoError(t, err)
	public, err := generation.ClaimPackage(importPath)
	require.NoError(t, err)
	require.NoError(t, public.DeclareName(goacodegen.NewExactName(
		goacodegen.NameFunction,
		"ValidateFindPayloadTransport",
	)))

	toolset := toolSpecsTestToolset(
		&agentexpr.ToolsetExpr{Name: "analytics", Tools: tools},
		"alpha.analytics",
		importPath,
		"gen/alpha/toolsets/analytics",
	)
	planned, err := planToolSpecs(generation, &ir.Design{
		GoaRoot:  goaRoot,
		Toolsets: []*ir.Toolset{toolset},
	}, goaRoot.API, nil)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())

	packages, err := planned.packageFor("gen/alpha/toolsets/analytics")
	require.NoError(t, err)
	payload := packages.tools["find"].payloadType
	publicUnionAttribute := goaexpr.AsObject(payload.publicShape.Type).Attribute("target")
	transportUnionAttribute := goaexpr.AsObject(payload.transportShape.Type).Attribute("target")
	publicBranch, err := packages.public.UnionBranch(publicUnionAttribute, "explicit")
	require.NoError(t, err)
	transportBranch, err := packages.transport.UnionBranch(transportUnionAttribute, "explicit")
	require.NoError(t, err)
	require.Equal(t, "NewTargetSeriesExplicit", publicBranch.Constructor())
	require.Equal(t, "NewTargetSeriesExplicit", transportBranch.Constructor())
	require.Equal(t, "ValidateFindPayloadTransport", payload.transportValidator.Name())
}

// TestPlanToolSpecsRejectsIndependentUnionsWithSameName checks that two
// separately written OneOf fields cannot silently share one Go type.
func TestPlanToolSpecsRejectsIndependentUnionsWithSameName(t *testing.T) {
	first := &goaexpr.Union{
		TypeName: "TargetSeries",
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "explicit", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
			{Name: "ranked", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Int}},
		},
	}
	second := &goaexpr.Union{
		TypeName: "TargetSeries",
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "explicit", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
			{Name: "ranked", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Int}},
		},
	}
	const importPath = "generated.local/gen/alpha/toolsets/analytics"
	goaRoot := toolSpecsTestGoaRoot("union-collision")
	generation, err := goacodegen.NewGeneration("generated.local/gen", []eval.Root{goaRoot})
	require.NoError(t, err)
	toolset := toolSpecsTestToolset(
		&agentexpr.ToolsetExpr{
			Name: "analytics",
			Tools: []*agentexpr.ToolExpr{
				{Name: "first", Args: &goaexpr.AttributeExpr{Type: first}},
				{Name: "second", Args: &goaexpr.AttributeExpr{Type: second}},
			},
		},
		"alpha.analytics",
		importPath,
		"gen/alpha/toolsets/analytics",
	)
	_, err = planToolSpecs(generation, &ir.Design{
		GoaRoot:  goaRoot,
		Toolsets: []*ir.Toolset{toolset},
	}, goaRoot.API, nil)
	require.ErrorContains(t, err, "set TypeName")
}

// toolSpecsTestToolset builds the complete definition and saved reference used
// by package-planning tests.
func toolSpecsTestToolset(expr *agentexpr.ToolsetExpr, name, importPath, dir string) *ir.Toolset {
	toolset := &ir.Toolset{
		Expr:            expr,
		Name:            name,
		SpecsImportPath: importPath,
		SpecsDir:        dir,
	}
	toolset.Owner.Ref = &ir.ToolsetRef{
		Expr:       expr,
		Definition: toolset,
	}
	return toolset
}

// TestLocalizeNestedTypesKeepsSourceIdentity checks that copies reuse one
// localized type while an unrelated equal-shaped type remains separate.
func TestLocalizeNestedTypesKeepsSourceIdentity(t *testing.T) {
	first := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
			{Name: "value", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		}},
		TypeName: "Filter",
		UID:      "first-filter",
	}
	second := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
			{Name: "value", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		}},
		TypeName: "Filter",
		UID:      "second-filter",
	}
	attribute := &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		{Name: "first", Attribute: &goaexpr.AttributeExpr{Type: first}},
		{Name: "first_copy", Attribute: &goaexpr.AttributeExpr{Type: first}},
		{Name: "second", Attribute: &goaexpr.AttributeExpr{Type: second}},
	}}

	localized, types := localizeNestedTypes(attribute, false, nil)

	require.Len(t, types, 2)
	object := goaexpr.AsObject(localized.Type)
	require.Same(t, object.Attribute("first").Type, object.Attribute("first_copy").Type)
	require.NotSame(t, object.Attribute("first").Type, object.Attribute("second").Type)
}

// TestUnionWalksKeepEqualNamedTypeOriginsDistinct checks that matching text IDs
// do not hide OneOf fields owned by different type declarations.
func TestUnionWalksKeepEqualNamedTypeOriginsDistinct(t *testing.T) {
	firstChoice := &goaexpr.AttributeExpr{Type: &goaexpr.Union{
		TypeName: "FirstChoice",
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "text", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		},
	}}
	secondChoice := &goaexpr.AttributeExpr{Type: &goaexpr.Union{
		TypeName: "SecondChoice",
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "text", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		},
	}}
	wrapped := func(choice *goaexpr.AttributeExpr) *goaexpr.UserTypeExpr {
		return &goaexpr.UserTypeExpr{
			TypeName: "Filter",
			UID:      "shared-filter-id",
			AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
				{Name: "choice", Attribute: choice},
			}},
		}
	}
	attribute := &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		{Name: "first", Attribute: &goaexpr.AttributeExpr{Type: wrapped(firstChoice)}},
		{Name: "second", Attribute: &goaexpr.AttributeExpr{Type: wrapped(secondChoice)}},
	}}
	generation, err := goacodegen.NewGeneration(
		"generated.local/gen",
		[]eval.Root{toolSpecsTestGoaRoot("union-walk-source-identity")},
	)
	require.NoError(t, err)
	pkg, err := generation.ClaimPackage("generated.local/gen/union-walk")
	require.NoError(t, err)
	helpers := make(map[goacodegen.UnionDeclarationID]*goacodegen.NameDeclaration)
	require.NoError(t, declareAttributeUnions(
		pkg,
		make(map[string]*goacodegen.NameDeclaration),
		helpers,
		attribute,
	))
	require.NoError(t, generation.Freeze())

	unions := make(map[goacodegen.UnionDeclarationID]*unionTypeData)
	collectUnionSumTypes(
		attribute,
		pkg.Scope(),
		pkg,
		helpers,
		unions,
		make(map[goaexpr.UserType]struct{}),
	)

	require.Len(t, unions, 2)
	require.Contains(t, unions, goacodegen.NewUnionDeclarationID(firstChoice))
	require.Contains(t, unions, goacodegen.NewUnionDeclarationID(secondChoice))
}

func TestToolSpecsPackagePlanSharesCodecTransformHelpers(t *testing.T) {
	child := toolSpecsTransformChild()
	attribute := toolSpecsTransformRoot(child, true)
	plan, generation := toolSpecsTransformPlan(t)
	first := &contractTypeOwner{
		Kind:          contractTypeOwnerTool,
		Name:          "first",
		QualifiedName: "helpers.first",
		ScopeName:     "helpers",
	}
	second := &contractTypeOwner{
		Kind:          contractTypeOwnerTool,
		Name:          "second",
		QualifiedName: "helpers.second",
		ScopeName:     "helpers",
	}
	require.NoError(t, plan.declareType(first, attribute, usagePayload, ""))
	require.NoError(t, plan.declareType(second, attribute, usagePayload, ""))
	require.NoError(t, plan.finalizeTransformHelpers())
	require.NoError(t, generation.Freeze())

	firstType := plan.types[stableTypeKey(first, usagePayload, "")]
	secondType := plan.types[stableTypeKey(second, usagePayload, "")]
	require.NotNil(t, firstType.decode.Helpers()[0].Declaration)
	require.NotNil(t, firstType.encode.Helpers()[0].Declaration)
	require.Same(t, firstType.decode.Helpers()[0].Declaration, secondType.decode.Helpers()[0].Declaration)
	require.Same(t, firstType.encode.Helpers()[0].Declaration, secondType.encode.Helpers()[0].Declaration)
}

func TestToolSpecsPackagePlanKeepsCollidingDSLToolsDistinct(t *testing.T) {
	plan, generation := toolSpecsTransformPlan(t)
	first := &contractTypeOwner{
		Kind:          contractTypeOwnerTool,
		Name:          "lookup1",
		QualifiedName: "helpers.lookup1",
		ScopeName:     "helpers",
	}
	second := &contractTypeOwner{
		Kind:          contractTypeOwnerTool,
		Name:          "lookup_1",
		QualifiedName: "helpers.lookup_1",
		ScopeName:     "helpers",
	}
	firstPayload := &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		&goaexpr.NamedAttributeExpr{Name: "query", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
	}}
	secondPayload := &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		&goaexpr.NamedAttributeExpr{Name: "session_id", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
	}}

	require.NoError(t, plan.declareType(first, firstPayload, usagePayload, ""))
	require.NoError(t, plan.declareType(second, secondPayload, usagePayload, ""))
	require.NoError(t, generation.Freeze())

	firstType := plan.types[stableTypeKey(first, usagePayload, "")]
	secondType := plan.types[stableTypeKey(second, usagePayload, "")]
	require.NotSame(t, firstType, secondType)
	require.NotEqual(t, firstType.publicDeclaration.Name(), secondType.publicDeclaration.Name())
	require.NotNil(t, firstType.publicShape.Find("query"))
	require.NotNil(t, secondType.publicShape.Find("session_id"))
}

// TestToolSpecsPackagePlanRetainsReferencePointers checks that later generator
// steps can use Goa's saved pointer choices without parsing rendered Go code.
func TestToolSpecsPackagePlanRetainsReferencePointers(t *testing.T) {
	tests := []struct {
		name      string
		attribute *goaexpr.AttributeExpr
		pointer   bool
	}{
		{
			name:      "object",
			attribute: &goaexpr.AttributeExpr{Type: &goaexpr.Object{}},
			pointer:   true,
		},
		{
			name: "union",
			attribute: &goaexpr.AttributeExpr{Type: &goaexpr.Union{
				TypeName: "Choice",
				Values: []*goaexpr.NamedAttributeExpr{
					{Name: "text", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
				},
			}},
			pointer: true,
		},
		{
			name: "primitive alias",
			attribute: &goaexpr.AttributeExpr{Type: &goaexpr.UserTypeExpr{
				TypeName:      "Label",
				AttributeExpr: &goaexpr.AttributeExpr{Type: goaexpr.String},
			}},
			pointer: false,
		},
		{
			name: "array",
			attribute: &goaexpr.AttributeExpr{Type: &goaexpr.Array{
				ElemType: &goaexpr.AttributeExpr{Type: goaexpr.String},
			}},
			pointer: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, generation := toolSpecsTransformPlan(t)
			owner := &contractTypeOwner{
				Kind:          contractTypeOwnerTool,
				Name:          test.name,
				QualifiedName: "helpers." + test.name,
				ScopeName:     "helpers",
			}

			require.NoError(t, plan.declareType(owner, test.attribute, usageResult, ""))
			require.NoError(t, generation.Freeze())

			planned := plan.types[stableTypeKey(owner, usageResult, "")]
			require.Equal(t, test.pointer, planned.publicLayout.ReferenceIsPointer())
			require.Equal(t, test.pointer, planned.transportLayout.ReferenceIsPointer())
		})
	}
}

func TestPlanToolSpecsKeepsGenerationRoots(t *testing.T) {
	exactGoa := toolSpecsTestGoaRoot("exact")
	exactMCP := mcpexpr.NewRoot()
	otherGoa := toolSpecsTestGoaRoot("other")
	otherMCP := mcpexpr.NewRoot()
	previousGoa := goaexpr.Root
	previousMCP := mcpexpr.Root
	goaexpr.Root = otherGoa
	mcpexpr.Root = otherMCP
	t.Cleanup(func() {
		goaexpr.Root = previousGoa
		mcpexpr.Root = previousMCP
	})

	generation, err := goacodegen.NewGeneration(
		"generated.local/gen",
		[]eval.Root{exactGoa, exactMCP},
	)
	require.NoError(t, err)
	planned, err := planToolSpecs(generation, &ir.Design{GoaRoot: exactGoa}, exactGoa.API, exactMCP)
	require.NoError(t, err)
	require.Same(t, exactGoa.API, planned.api)
	require.Same(t, exactMCP, planned.mcp)
}

func TestToolExpressionsForReferenceUsesPassedMCPRoot(t *testing.T) {
	exact := mcpexpr.NewRoot()
	exact.MCPServers["calc"] = &mcpexpr.MCPExpr{
		Name:  "calc-mcp",
		Tools: []*mcpexpr.ToolExpr{{Name: "exact"}},
	}
	other := mcpexpr.NewRoot()
	other.MCPServers["calc"] = &mcpexpr.MCPExpr{
		Name:  "calc-mcp",
		Tools: []*mcpexpr.ToolExpr{{Name: "other"}},
	}
	previous := mcpexpr.Root
	mcpexpr.Root = other
	t.Cleanup(func() { mcpexpr.Root = previous })

	provider := &agentexpr.ProviderExpr{
		Kind:       agentexpr.ProviderMCP,
		MCPService: "calc",
		MCPToolset: "calc-mcp",
		MCPSource:  agentexpr.MCPSourceGoa,
	}
	definition := &ir.Toolset{
		Expr: &agentexpr.ToolsetExpr{Provider: provider},
	}
	reference := &ir.ToolsetRef{
		Name:       "remote",
		Expr:       definition.Expr,
		Definition: definition,
		Provider: &ir.ToolsetProvider{
			Kind: agentexpr.ProviderMCP,
			MCP:  &ir.MCPToolsetMeta{Source: agentexpr.MCPSourceGoa},
		},
	}
	tools, err := toolExpressionsForReference(exact, reference)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Equal(t, "exact", tools[0].Name)
}

func TestSchemaVariantsUsesPassedAPI(t *testing.T) {
	exact := goaexpr.NewAPIExpr("exact", nil)
	previous := goaexpr.Root
	goaexpr.Root = &goaexpr.RootExpr{}
	t.Cleanup(func() { goaexpr.Root = previous })

	attribute := &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		&goaexpr.NamedAttributeExpr{
			Name:      "message",
			Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String},
		},
	}}
	identity := goaexpr.UserTypeExampleIdentity(&goaexpr.UserTypeExpr{
		TypeName: "ExactToolPayload",
		UID:      "test:exact-tool-payload",
	})
	withExample, withoutExample, err := schemaVariantsForAttribute(exact, attribute, nil, identity)
	require.NoError(t, err)
	require.NotEmpty(t, withExample)
	require.NotEmpty(t, withoutExample)
}

// toolSpecsTransformPlan returns an empty tool package plan used to compare
// generated conversion function declarations.
func toolSpecsTransformPlan(t *testing.T) (*toolSpecsPackagePlan, *goacodegen.Generation) {
	t.Helper()
	generation, err := goacodegen.NewGeneration("generated.local/gen", []eval.Root{toolSpecsTestGoaRoot("transform-helpers")})
	require.NoError(t, err)
	public, err := generation.ClaimPackage("generated.local/gen/helpers/specs")
	require.NoError(t, err)
	transport, err := generation.ClaimPackage("generated.local/gen/helpers/specs/http")
	require.NoError(t, err)
	return newToolSpecsPackagePlan(generation, "generated.local/gen", public, transport), generation
}

// toolSpecsTransformChild returns the named nested object converted by each
// test plan.
func toolSpecsTransformChild() *goaexpr.UserTypeExpr {
	return &goaexpr.UserTypeExpr{
		TypeName: "SharedChild",
		AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
			&goaexpr.NamedAttributeExpr{
				Name:      "value",
				Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String},
			},
		}},
	}
}

// toolSpecsTransformRoot returns an object whose nested child is required or
// optional according to required.
func toolSpecsTransformRoot(child goaexpr.UserType, required bool) *goaexpr.AttributeExpr {
	attribute := &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		&goaexpr.NamedAttributeExpr{
			Name:      "child",
			Attribute: &goaexpr.AttributeExpr{Type: child},
		},
	}}
	if required {
		attribute.Validation = &goaexpr.ValidationExpr{Required: []string{"child"}}
	}
	return attribute
}

// toolSpecsTestGoaRoot returns one Goa root with its own API value.
func toolSpecsTestGoaRoot(name string) *goaexpr.RootExpr {
	return &goaexpr.RootExpr{API: goaexpr.NewAPIExpr(name, nil)}
}
