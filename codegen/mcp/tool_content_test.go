// These tests exercise the authored content declaration and its selected result
// contracts. Generated HTTP tests use the same fixtures to verify actual codecs.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/internal/mcpcontract"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

// toolContentFixture reuses the prompt's five typed content branches. One tool
// returns domain fields beside content; another returns content alone.
func toolContentFixture(methods map[string]*expr.MethodExpr) []expr.UserType {
	prompt := expr.AsObject(methods["review"].Result.Type)
	messages := expr.AsArray(prompt.Attribute("messages").Type)
	content := expr.AsObject(messages.ElemType.Type).Attribute("content")
	item := promptFixtureType("ToolAttachment", &expr.Object{{Name: "content", Attribute: content}}, "content")
	attachments := &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: item}, NonNullableElems: true}, Description: "Ordered content shown according to its audience"}
	attachments.Meta = expr.MetaExpr{"struct:field:name": {"Attachments"}}
	shape := promptFixtureType("RichToolResult", &expr.Object{
		{Name: "summary", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Structured domain result"}},
		{Name: "attachments", Attribute: attachments},
	}, "summary")
	shape.UserExamples = []*expr.ExampleExpr{{Value: expr.Val{"summary": "domain example", "attachments": []any{expr.Val{"content": expr.Val{"type": "text", "value": expr.Val{"text": "presentation example"}}}}}}}
	result := fixedViewFixtureResult(shape)
	methods["rich"].Payload = &expr.AttributeExpr{Type: expr.Empty}
	methods["rich"].Result = &expr.AttributeExpr{Type: result}
	only := promptFixtureType("ContentOnlyResult", &expr.Object{{Name: "attachments", Attribute: attachments}})
	methods["content_only"].Payload = &expr.AttributeExpr{Type: expr.Empty}
	methods["content_only"].Result = &expr.AttributeExpr{Type: only}
	return []expr.UserType{result, only, item}
}

func TestToolContentSelectedContracts(t *testing.T) {
	service, methods := testService("content_tools", "review", "empty", "rich", "content_only")
	methodPromptFixture(methods)
	toolContentFixture(methods)
	rich := &mcpexpr.ToolExpr{Name: "rich", Description: "Return content", Method: methods["rich"], ContentField: "attachments"}
	only := &mcpexpr.ToolExpr{Name: "only", Description: "Return content alone", Method: methods["content_only"], ContentField: "attachments"}
	definition := &mcpexpr.MCPExpr{Tools: []*mcpexpr.ToolExpr{rich, only}}
	adapters, err := newAdapterGenerator(testSchemaAPI(), service, definition).buildToolAdapters()
	require.NoError(t, err)
	assert.NotContains(t, adapters[1].OutputSchema, "attachments")
	assert.Contains(t, adapters[1].OutputSchema, "summary")
	assert.Empty(t, adapters[0].OutputSchema)
	original := expr.AsObject(methods["rich"].Result.Type)
	assert.NotNil(t, original.Attribute("attachments"))
	result := methods["rich"].Result.Type.(*expr.ResultTypeExpr)
	result.Views = append(result.Views, &expr.ViewExpr{Name: "summary", Parent: result, AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{{Name: "summary", Attribute: original.Attribute("summary")}}}})
	contract, err := mcpcontract.ToolResult(rich)
	require.NoError(t, err)
	branches := expr.AsUnion(contract.Type)
	require.Len(t, branches.Values, 2)
	for _, branch := range branches.Values {
		assert.Nil(t, expr.AsObject(branch.Attribute.Type).Attribute("attachments"))
		assert.NotNil(t, expr.AsObject(branch.Attribute.Type).Attribute("summary"))
	}
	authored, err := newAdapterGenerator(testSchemaAPI(), service, definition).buildToolResultAdapter(rich)
	require.NoError(t, err)
	assert.NotNil(t, authored.Cases[0].conversion)
	assert.Nil(t, authored.Cases[1].conversion)
}

func TestToolContentAuthoringRejectsInvalidShapes(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*expr.MethodExpr)
		want   string
	}{
		{name: "missing", change: func(method *expr.MethodExpr) { method.Result = &expr.AttributeExpr{Type: expr.String} }, want: "must name a field"},
		{name: "nullable elements", change: func(method *expr.MethodExpr) {
			expr.AsArray(expr.AsObject(method.Result.Type).Attribute("attachments").Type).NonNullableElems = false
		}, want: "ArrayOfRequired"},
		{name: "required empty", change: func(method *expr.MethodExpr) {
			method.Result.Validation = &expr.ValidationExpr{Required: []string{"attachments"}}
		}, want: "MinLength(1)"},
		{name: "unknown variant", change: func(method *expr.MethodExpr) {
			expr.AsUnion(expr.AsObject(expr.AsArray(expr.AsObject(method.Result.Type).Attribute("attachments").Type).ElemType.Type).Attribute("content").Type).Values[0].Name = "video"
		}, want: "unsupported content branch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, methods := testService("content_tools", "review", "empty", "rich", "content_only")
			methodPromptFixture(methods)
			toolContentFixture(methods)
			test.change(methods["rich"])
			tool := &mcpexpr.ToolExpr{Name: "rich", Description: "Return content", Method: methods["rich"], ContentField: "attachments"}
			_, err := newAdapterGenerator(testSchemaAPI(), service, &mcpexpr.MCPExpr{Tools: []*mcpexpr.ToolExpr{tool}}).buildToolAdapters()
			assert.ErrorContains(t, err, test.want)
		})
	}
}

// TestToolContentPreservesDomainFieldsAndExamples verifies that only the marked
// attachment field is excluded, even when a domain field is named content.
func TestToolContentPreservesDomainFieldsAndExamples(t *testing.T) {
	_, methods := testService("content_tools", "review", "empty", "rich", "content_only")
	methodPromptFixture(methods)
	toolContentFixture(methods)
	result := methods["rich"].Result.Type.(*expr.ResultTypeExpr)
	field := &expr.NamedAttributeExpr{Name: "content", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Ordinary domain value"}}
	object := expr.AsObject(result.Type)
	*object = append(*object, field)
	view := expr.AsObject(result.Views[0].Type)
	*view = append(*view, field)
	result.Views[0].UserExamples = []*expr.ExampleExpr{{Value: expr.Val{"summary": "domain", "content": "ordinary", "attachments": []any{}}}}
	tool := &mcpexpr.ToolExpr{Method: methods["rich"], ContentField: "attachments"}
	selected, err := mcpcontract.ToolResult(tool)
	require.NoError(t, err)
	assert.NotNil(t, expr.AsObject(selected.Type).Attribute("content"))
	assert.Nil(t, expr.AsObject(selected.Type).Attribute("attachments"))
	examples := selected.ExtractUserExamples()
	require.Len(t, examples, 1)
	assert.Equal(t, expr.Val{"summary": "domain", "content": "ordinary"}, examples[0].Value)
	assert.Contains(t, result.Views[0].UserExamples[0].Value, "attachments")
	assert.NotNil(t, object.Attribute("attachments"))
	assert.NotNil(t, view.Attribute("attachments"))
}
