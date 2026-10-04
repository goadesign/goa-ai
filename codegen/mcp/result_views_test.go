// These tests reject authored views that cannot produce the required prompt,
// resource or completion fields before a server can be generated.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

func TestMCPExecutionSelectedViewsRequireProtocolFields(t *testing.T) {
	for _, name := range []string{"prompt", "resource", "completion"} {
		t.Run(name, func(t *testing.T) {
			service, methods := testService("view_contract", "review", "empty", "complete", "read")
			methodPromptFixture(methods)
			resourceReaderFixture(methods["read"])
			promptCompletionFixture(methods["complete"])
			definition := &mcpexpr.MCPExpr{Name: "views", Version: "1"}
			var method *expr.MethodExpr
			switch name {
			case "prompt":
				method = methods["review"]
				definition.MethodPrompts = []*mcpexpr.MethodPromptExpr{{Name: "review", Description: "Return a synthetic prompt", Method: method}}
			case "resource":
				method = methods["read"]
				definition.ResourceTemplates = []*mcpexpr.ResourceTemplateExpr{{Name: "records", URI: "test://records/{id}", MimeType: "text/plain", Method: method}}
			case "completion":
				method = methods["complete"]
				definition.PromptCompletions = []*mcpexpr.PromptCompletionExpr{{Prompt: "review", Argument: "code", Method: method}}
			}
			result := method.Result.Type.(*expr.ResultTypeExpr)
			result.Views = append(result.Views, &expr.ViewExpr{Name: "missing_fields", Parent: result, AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{}}})
			generator := newAdapterGenerator(testSchemaAPI(), service, definition)
			var err error
			switch name {
			case "prompt":
				_, err = generator.buildMethodPromptAdapters()
			case "resource":
				_, err = generator.buildResourceReaderAdapter()
			case "completion":
				_, err = generator.buildCompletionAdapters()
			}
			assert.ErrorContains(t, err, "missing_fields")
		})
	}
}
