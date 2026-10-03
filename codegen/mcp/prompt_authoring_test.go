// These tests reject authored content that cannot retain its typed fields in an
// MCP response. Stronger domain validation remains valid for supported fields.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

func TestMethodPromptAuthoring(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*expr.MethodExpr, *expr.Object, *expr.Union)
		want   string
	}{
		{name: "complete typed result"},
		{name: "stronger bounds", change: func(_ *expr.MethodExpr, _ *expr.Object, content *expr.Union) {
			priority := expr.AsObject(expr.AsObject(content.Values[0].Attribute.Type).Attribute("annotations").Type).Attribute("priority")
			priority.Validation = &expr.ValidationExpr{ExclusiveMinimum: new(0.0), ExclusiveMaximum: new(1.0)}
		}},
		{name: "nullable messages", change: func(method *expr.MethodExpr, _ *expr.Object, _ *expr.Union) {
			expr.AsArray(expr.AsObject(method.Result.Type).Attribute("messages").Type).NonNullableElems = false
		}, want: "ArrayOfRequired"},
		{name: "missing role enum", change: func(_ *expr.MethodExpr, message *expr.Object, _ *expr.Union) {
			message.Attribute("role").Type = expr.String
		}, want: "role must declare Enum"},
		{name: "unsupported role", change: func(_ *expr.MethodExpr, message *expr.Object, _ *expr.Union) {
			message.Attribute("role").Type = expr.String
			message.Attribute("role").Validation = &expr.ValidationExpr{Values: []any{"system"}}
		}, want: "unsupported value system"},
		{name: "unknown content", change: func(_ *expr.MethodExpr, _ *expr.Object, content *expr.Union) { content.Values[0].Name = "video" }, want: "unsupported content branch"},
		{name: "undeclared text", change: func(_ *expr.MethodExpr, _ *expr.Object, content *expr.Union) {
			content.Values[0].Attribute.Type = expr.String
		}, want: "must be an object"},
		{name: "extra content field", change: func(_ *expr.MethodExpr, _ *expr.Object, content *expr.Union) {
			object := expr.AsObject(content.Values[0].Attribute.Type)
			*object = append(*object, &expr.NamedAttributeExpr{Name: "secret", Attribute: &expr.AttributeExpr{Type: expr.String}})
		}, want: "has no MCP representation"},
		{name: "string media", change: func(_ *expr.MethodExpr, _ *expr.Object, content *expr.Union) {
			expr.AsObject(content.Values[1].Attribute.Type).Attribute("data").Type = expr.String
		}, want: "must use Bytes"},
		{name: "missing URI validation", change: func(_ *expr.MethodExpr, _ *expr.Object, content *expr.Union) {
			expr.AsObject(content.Values[3].Attribute.Type).Attribute("uri").Validation = nil
		}, want: "MCP field validation"},
		{name: "opaque string argument", change: func(method *expr.MethodExpr, _ *expr.Object, _ *expr.Union) {
			expr.AsObject(method.Payload.Type).Attribute("code").Meta = expr.MetaExpr{"struct:field:type": {"custom.Code", "example.com/custom"}}
		}, want: "struct:field:type is unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, methods := testService("prompts", "review", "empty")
			methodPromptFixture(methods)
			method := methods["review"]
			message := expr.AsObject(expr.AsArray(expr.AsObject(method.Result.Type).Attribute("messages").Type).ElemType.Type)
			content := expr.AsUnion(message.Attribute("content").Type)
			if test.change != nil {
				test.change(method, message, content)
			}
			server := &mcpexpr.MCPExpr{MethodPrompts: []*mcpexpr.MethodPromptExpr{{Name: "review", Description: "Review", Method: method}}}
			generated, err := newAdapterGenerator(nil, service, server).buildMethodPromptAdapters()
			if test.want != "" {
				assert.ErrorContains(t, err, test.want)
				return
			}
			require.NoError(t, err)
			assert.Len(t, generated, 1)
			assert.Len(t, generated[0].conversion.branches, 5)
		})
	}
}
