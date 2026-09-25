// Package codec tests the automatic original-value planner independently of
// discovery: eligibility, shared declarations and the generated public surface.
package codec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

func TestSupportsStandaloneCompleteGraph(t *testing.T) {
	stringKey := standaloneTestType("Key", expr.String)
	intKey := standaloneTestType("IntKey", expr.Int64)
	for _, test := range []struct {
		name string
		attr *expr.AttributeExpr
		want bool
	}{
		{"string", &expr.AttributeExpr{Type: expr.String}, true},
		{"any", &expr.AttributeExpr{Type: expr.Any}, false},
		{"builtin error", &expr.AttributeExpr{Type: expr.ErrorResult}, false},
		{"named string keys", &expr.AttributeExpr{Type: &expr.Map{
			KeyType: &expr.AttributeExpr{Type: stringKey}, ElemType: &expr.AttributeExpr{Type: expr.Bytes},
		}}, true},
		{"named integer keys", &expr.AttributeExpr{Type: &expr.Map{
			KeyType: &expr.AttributeExpr{Type: intKey}, ElemType: &expr.AttributeExpr{Type: expr.String},
		}}, false},
		{"optional any field", &expr.AttributeExpr{Type: &expr.Object{
			{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.Any}},
		}}, false},
		{"nested builtin error", &expr.AttributeExpr{Type: &expr.Array{
			ElemType: &expr.AttributeExpr{Type: expr.ErrorResult},
		}}, false},
		{"union any branch", &expr.AttributeExpr{Type: &expr.Union{
			TypeName: "Choice", Values: []*expr.NamedAttributeExpr{
				{Name: "known", Attribute: &expr.AttributeExpr{Type: expr.String}},
				{Name: "dynamic", Attribute: &expr.AttributeExpr{Type: expr.Any}},
			},
		}}, false},
		{"union empty branch", &expr.AttributeExpr{Type: &expr.Union{
			TypeName: "EmptyChoice", Values: []*expr.NamedAttributeExpr{
				{Name: "empty", Attribute: &expr.AttributeExpr{Type: expr.Empty}},
			},
		}}, true},
		{"custom occurrence", &expr.AttributeExpr{
			Type: stringKey, Meta: expr.MetaExpr{"struct:field:type": {"custom.Key", "example.com/custom"}},
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, SupportsStandalone(test.attr))
		})
	}
}

func TestSupportsStandaloneCyclesAreRootLocal(t *testing.T) {
	a := standaloneTestType("A", &expr.Object{})
	b := standaloneTestType("B", &expr.Object{})
	a.Type = &expr.Object{
		{Name: "b", Attribute: &expr.AttributeExpr{Type: b}},
		{Name: "unsupported", Attribute: &expr.AttributeExpr{Type: expr.Any}},
	}
	b.Type = &expr.Object{{Name: "a", Attribute: &expr.AttributeExpr{Type: a}}}
	assert.False(t, SupportsStandalone(&expr.AttributeExpr{Type: a}))
	assert.False(t, SupportsStandalone(&expr.AttributeExpr{Type: b}))
	(*a.AttributeExpr.Type.(*expr.Object))[1].Attribute.Type = expr.String
	assert.True(t, SupportsStandalone(&expr.AttributeExpr{Type: a}))
	assert.True(t, SupportsStandalone(&expr.AttributeExpr{Type: b}))

	// A custom occurrence must be checked even after an ordinary occurrence of
	// the same named type was visited.
	object := &expr.AttributeExpr{Type: &expr.Object{
		{Name: "ordinary", Attribute: &expr.AttributeExpr{Type: a}},
		{Name: "custom", Attribute: &expr.AttributeExpr{
			Type: a, Meta: expr.MetaExpr{"struct:field:type": {"custom.A", "example.com/custom"}},
		}},
	}}
	assert.False(t, SupportsStandalone(object))
}

func TestStandaloneSharesNamedGraphAndOccurrenceRules(t *testing.T) {
	node := standaloneTestType("Node", &expr.Object{})
	node.Type = &expr.Object{
		{Name: "next", Attribute: &expr.AttributeExpr{Type: node}},
		{Name: "label", Attribute: &expr.AttributeExpr{Type: expr.String}},
	}
	nullable := &expr.Array{ElemType: &expr.AttributeExpr{Type: node}}
	strict := &expr.Array{ElemType: &expr.AttributeExpr{Type: node}, NonNullableElems: true}
	root := standaloneTestType("Root", &expr.Object{
		{Name: "nullable", Attribute: &expr.AttributeExpr{Type: nullable}},
		{Name: "strict", Attribute: &expr.AttributeExpr{Type: strict}},
	})
	generation, owner, plan := standaloneTestPlan(t, root, node)
	rootValue := addStandaloneTestValue(t, generation, owner, plan, root)
	nodeValue := addStandaloneTestValue(t, generation, owner, plan, node)
	require.Len(t, plan.originals.types, 2)
	require.Len(t, rootValue.types, 2)
	require.Len(t, nodeValue.types, 1, "root seeding must avoid a second recursive Node copy")
	assert.Same(t, rootValue.types[1], nodeValue.types[0])
	assert.Same(t, rootValue.types[1].validation, nodeValue.types[0].validation)
	fields := expr.AsObject(rootValue.transport.Type)
	gotNullable := expr.AsArray(fields.Attribute("nullable").Type)
	gotStrict := expr.AsArray(fields.Attribute("strict").Type)
	assert.False(t, gotNullable.NonNullableElems)
	assert.True(t, gotStrict.NonNullableElems)
	assert.Same(t, gotNullable.ElemType.Type, gotStrict.ElemType.Type)
	assert.Same(t, node, nullable.ElemType.Type, "the original graph must not be mutated")
	source := renderStandaloneTestPlan(t, generation, owner, plan)
	assert.Equal(t, 1, strings.Count(source, "type jsonNodeTransport "))
	assert.Equal(t, 1, strings.Count(source, "func validatejsonNodeTransport("))
	assert.NotContains(t, source, `"`+owner.ImportPath()+`"`)
}

func TestStandalonePrivateNamesAndImportAliases(t *testing.T) {
	choice := &expr.Union{
		TypeName: "Choice", Values: []*expr.NamedAttributeExpr{
			{Name: "text", Attribute: &expr.AttributeExpr{Type: expr.String}},
			{Name: "empty", Attribute: &expr.AttributeExpr{Type: expr.Empty}},
		},
	}
	root := standaloneTestType("Root", &expr.Object{
		{Name: "choice", Attribute: &expr.AttributeExpr{Type: choice}},
	})
	generation, owner, plan := standaloneTestPlan(t, root)
	require.NoError(t, owner.DeclareName(codegen.NewExactName(codegen.NameFunction, "readStrictJSON")))
	require.NoError(t, owner.DeclareName(codegen.NewExactName(codegen.NameFunction, "EncodeRoot")))
	require.NoError(t, owner.RequireImport(codegen.NewImport("json", "example.com/other/json")))
	require.NoError(t, owner.RequireImport(codegen.NewImport("formatting", "fmt")))
	value := addStandaloneTestValue(t, generation, owner, plan, root)
	source := renderStandaloneTestPlan(t, generation, owner, plan)
	assert.NotEqual(t, "EncodeRoot", value.EncodeDeclaration().Name())
	assert.NotEqual(t, "readStrictJSON", plan.jsonHelpers.read.Name())
	assert.Contains(t, source, "json2.Valid(data)")
	assert.Contains(t, source, "formatting.Errorf(")
	assert.NotContains(t, source, "fmt.Errorf(")
	assert.Contains(t, source, "func "+plan.jsonHelpers.read.Name()+"(")

	file, err := parser.ParseFile(token.NewFileSet(), "codec.go", source, 0)
	require.NoError(t, err)
	var exported []string
	for _, declaration := range file.Decls {
		switch actual := declaration.(type) {
		case *ast.FuncDecl:
			if actual.Recv == nil && actual.Name.IsExported() {
				exported = append(exported, actual.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range actual.Specs {
				switch actual := spec.(type) {
				case *ast.TypeSpec:
					assert.False(t, actual.Name.IsExported(), actual.Name.Name)
				case *ast.ValueSpec:
					for _, name := range actual.Names {
						assert.False(t, name.IsExported(), name.Name)
					}
				}
			}
		}
	}
	assert.ElementsMatch(t, []string{value.EncodeDeclaration().Name(), value.DecodeDeclaration().Name()}, exported)
}

func TestStandaloneChainEmitsOneTransportDefinitionPerOriginal(t *testing.T) {
	const count = 8
	types := make([]expr.UserType, count)
	var next expr.UserType
	for i := count - 1; i >= 0; i-- {
		fields := &expr.Object{{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String}}}
		if next != nil {
			*fields = append(*fields, &expr.NamedAttributeExpr{Name: "next", Attribute: &expr.AttributeExpr{Type: next}})
		}
		types[i] = standaloneTestType("Chain"+strconv.Itoa(i), fields)
		next = types[i]
	}
	generation, owner, plan := standaloneTestPlan(t, types[0])
	for _, userType := range types {
		addStandaloneTestValue(t, generation, owner, plan, userType)
	}
	source := renderStandaloneTestPlan(t, generation, owner, plan)
	assert.Equal(t, count, strings.Count(source, "type jsonChain"))
	assert.Equal(t, count, strings.Count(source, "func validatejsonChain"))
}

func TestStandaloneSameOriginDifferentOwners(t *testing.T) {
	shared := standaloneTestType("Shared", expr.String)
	generation, owner, plan := standaloneTestPlan(t, shared)
	other, err := generation.ClaimPackage("example.com/gen/other")
	require.NoError(t, err)
	otherDeclaration, err := other.DeclareUserType(shared)
	require.NoError(t, err)
	first := addStandaloneTestValue(t, generation, owner, plan, shared).transport
	second, err := plan.copyOriginalTransport(&expr.AttributeExpr{Type: shared}, other.ImportPath())
	require.NoError(t, err)
	assert.NotSame(t, first.Type, second.Type)
	assert.NotNil(t, plan.originals.types[otherDeclaration])
	assert.Len(t, plan.originals.types, 2)
}

func standaloneTestType(name string, dataType expr.DataType) *expr.UserTypeExpr {
	return &expr.UserTypeExpr{TypeName: name, UID: "standalone-test:" + name, AttributeExpr: &expr.AttributeExpr{Type: dataType}}
}

func standaloneTestPlan(t *testing.T, types ...expr.UserType) (*codegen.Generation, *codegen.GeneratedPackage, *Plan) {
	t.Helper()
	generation, err := codegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	owner, err := generation.ClaimPackage("example.com/gen/types")
	require.NoError(t, err)
	for _, userType := range types {
		attribute := &expr.AttributeExpr{Type: userType}
		require.NoError(t, walkAttribute(attribute, make(map[expr.UserType]struct{}), func(current *expr.AttributeExpr) error {
			if named, ok := current.Type.(expr.UserType); ok && named != expr.Empty {
				_, err := owner.DeclareUserType(named)
				return err
			}
			return nil
		}))
		require.NoError(t, walkAttribute(attribute, make(map[expr.UserType]struct{}), func(current *expr.AttributeExpr) error {
			if _, ok := current.Type.(*expr.Union); ok {
				_, err := owner.DeclareUnion(current)
				return err
			}
			return nil
		}))
	}
	plan, err := NewPlan(generation, owner.ImportPath(), owner.ImportPath())
	require.NoError(t, err)
	return generation, owner, plan
}

func addStandaloneTestValue(t *testing.T, generation *codegen.Generation, owner *codegen.GeneratedPackage, plan *Plan, userType expr.UserType) *Value {
	t.Helper()
	attribute := &expr.AttributeExpr{Type: userType}
	require.True(t, SupportsStandalone(attribute))
	layout, err := codegen.PlanGoType(attribute, codegen.GoTypePlanOptions{
		Owner: owner.ImportPath(), Policy: codegen.GoLayoutPolicy{UseDefault: true, SumType: true},
		RetainNamedValue: true,
		Bind: func(request codegen.GoTypeBindingRequest) (codegen.GoTypeBinding, error) {
			ownerPath := request.InheritedOwner
			if location := codegen.UserTypeLocation(request.Attribute.Type); location != nil {
				ownerPath = path.Join(generation.GenPkg(), location.RelImportPath)
			}
			pkg := generation.Package(ownerPath)
			binding := codegen.GoTypeBinding{Owner: ownerPath}
			var err error
			if request.Kind == codegen.GoUnion {
				binding.Union, err = pkg.Union(request.Attribute)
			} else {
				binding.Type, err = pkg.Type(request.Attribute.Type.(expr.UserType))
			}
			return binding, err
		},
	})
	require.NoError(t, err)
	value, err := plan.AddStandalone(userType.Name(), userType.Name(), attribute, layout)
	require.NoError(t, err)
	return value
}

func renderStandaloneTestPlan(t *testing.T, generation *codegen.Generation, owner *codegen.GeneratedPackage, plan *Plan) string {
	t.Helper()
	require.NoError(t, generation.Freeze())
	for _, value := range plan.values {
		context := &codegen.AttributeContext{UseDefault: true, Scope: codegen.NewAttributeScope(owner.Scope())}
		context, err := context.WithGoTypeLayout(value.originalLayout.Link(owner.ImportPath(), owner.ImportName))
		require.NoError(t, err)
		require.NoError(t, value.BindService(context.Scope))
	}
	files, err := plan.Files("types")
	require.NoError(t, err)
	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	return readOnlyGoFile(t, directory)
}
