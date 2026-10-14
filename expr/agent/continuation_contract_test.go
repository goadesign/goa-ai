package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestContinuationRetainedFieldContracts(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ToolExpr, *ToolExpr)
		valid  bool
	}{
		{name: "required workspace subset", valid: true},
		{name: "optional field absent on both", valid: true, change: func(source, target *ToolExpr) {
			source.Args.Validation.Required = nil
			target.Args.Validation.Required = []string{"cursor"}
		}},
		{name: "required target optional source", change: func(source, target *ToolExpr) { source.Args.Validation.Required = nil }},
		{name: "provider inject on both", valid: true, change: func(source, target *ToolExpr) {
			source.InjectedFields = []string{"workspace"}
			target.InjectedFields = []string{"workspace"}
		}},
		{name: "hidden source unavailable", change: func(source, target *ToolExpr) {
			source.Args.Find("workspace").Meta = goaexpr.MetaExpr{"struct:tag:json": []string{"-"}}
		}},
		{name: "both execution hidden", valid: true, change: func(source, target *ToolExpr) {
			source.Args.Find("workspace").Meta = goaexpr.MetaExpr{"struct:tag:json": []string{"-,omitempty"}}
			target.Args.Find("workspace").Meta = goaexpr.MetaExpr{"struct:tag:json:name": []string{"-"}}
		}},
		{name: "target execution hidden", valid: true, change: func(source, target *ToolExpr) {
			target.Args.Find("workspace").Meta = goaexpr.MetaExpr{"struct:tag:json": []string{"-"}}
		}},
		{name: "injected source unavailable", change: func(source, target *ToolExpr) { source.InjectedFields = []string{"workspace"} }},
		{name: "missing source field", change: func(source, target *ToolExpr) { (*goaexpr.AsObject(source.Args.Type))[0].Name = "other" }},
		{name: "narrower validation", change: func(source, target *ToolExpr) {
			target.Args.Find("workspace").Validation = &goaexpr.ValidationExpr{MinLength: new(3)}
		}},
		{name: "changed default", change: func(source, target *ToolExpr) { target.Args.Find("workspace").DefaultValue = "other" }},
		{name: "wire name collision", change: func(source, target *ToolExpr) {
			obj := goaexpr.AsObject(source.Args.Type)
			*obj = append(*obj, &goaexpr.NamedAttributeExpr{Name: "workSpace", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}}, &goaexpr.NamedAttributeExpr{Name: "work_space", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, target := continuationContractFixture()
			if test.change != nil {
				test.change(source, target)
			}
			errors := new(eval.ValidationErrors)
			validateContinuationPayloadMatch(source, target, errors)
			assert.Equal(t, test.valid, len(errors.Errors) == 0, errors.Error())
		})
	}
}

func TestContinuationEffectiveNestedValueContract(t *testing.T) {
	primitive := func() *goaexpr.AttributeExpr {
		return &goaexpr.AttributeExpr{Type: goaexpr.String, Validation: &goaexpr.ValidationExpr{MinLength: new(2)}, DefaultValue: "ab"}
	}
	alias := func(name string, inner *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
		return &goaexpr.AttributeExpr{Type: &goaexpr.UserTypeExpr{TypeName: name, AttributeExpr: inner}}
	}
	equal := func(left, right *goaexpr.AttributeExpr) bool {
		return continuationValueEqual(left, right, make(map[continuationAttributePair]bool))
	}
	assert.True(t, equal(alias("SourceName", primitive()), alias("TargetName", primitive())))
	narrower := primitive()
	narrower.Validation.MinLength = new(3)
	assert.False(t, equal(alias("SourceName", primitive()), alias("TargetName", narrower)))
	object := func(required bool) *goaexpr.AttributeExpr {
		value := &goaexpr.AttributeExpr{Type: &goaexpr.Object{&goaexpr.NamedAttributeExpr{Name: "name", Attribute: primitive()}}}
		if required {
			value.Validation = &goaexpr.ValidationExpr{Required: []string{"name"}}
		}
		return value
	}
	assert.True(t, equal(object(true), object(true)))
	assert.False(t, equal(object(true), object(false)))
	leftHidden, rightHidden := object(true), object(true)
	leftHidden.Find("name").Meta = goaexpr.MetaExpr{"struct:tag:json": []string{"-"}}
	assert.False(t, equal(leftHidden, rightHidden), "nested execution visibility must match")
	rightHidden.Find("name").Meta = goaexpr.MetaExpr{"struct:tag:json:name": []string{"-"}}
	assert.True(t, equal(leftHidden, rightHidden), "matching hidden nested fields preserve the wire shape")

	assert.False(t, equal(&goaexpr.AttributeExpr{Type: &goaexpr.Array{ElemType: primitive(), NonNullableElems: true}}, &goaexpr.AttributeExpr{Type: &goaexpr.Array{ElemType: primitive()}}))
	assert.False(t, equal(&goaexpr.AttributeExpr{Type: &goaexpr.Map{KeyType: primitive(), ElemType: primitive()}}, &goaexpr.AttributeExpr{Type: &goaexpr.Map{KeyType: primitive(), ElemType: narrower}}))
	union := func() *goaexpr.AttributeExpr {
		return &goaexpr.AttributeExpr{Type: &goaexpr.Union{TypeName: "Choice", Values: []*goaexpr.NamedAttributeExpr{{Name: "record", Attribute: object(true)}}}}
	}
	changed := union()
	changed.Type.(*goaexpr.Union).Flatten = true
	assert.False(t, equal(union(), changed))
	renamed := union()
	renamed.Type.(*goaexpr.Union).TypeName = "OtherChoice"
	assert.True(t, equal(union(), renamed))
	custom := primitive()
	custom.Meta = goaexpr.MetaExpr{"struct:field:type": []string{"custom.Value", "example.com/custom"}}
	assert.False(t, equal(primitive(), custom))
	assert.True(t, equal(custom, goaexpr.DupAtt(custom)))
}

func continuationContractFixture() (*ToolExpr, *ToolExpr) {
	source := &ToolExpr{Name: "search", Args: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		{Name: "workspace", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		{Name: "query", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
	}, Validation: &goaexpr.ValidationExpr{Required: []string{"workspace", "query"}}}}
	target := &ToolExpr{Name: "continue_search", Args: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		{Name: "workspace", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		{Name: "cursor", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
	}, Validation: &goaexpr.ValidationExpr{Required: []string{"workspace", "cursor"}}}, Bounds: &ToolBoundsExpr{Paging: &ToolPagingExpr{CursorField: "cursor"}}}
	return source, target
}
