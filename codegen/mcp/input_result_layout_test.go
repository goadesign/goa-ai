// These tests keep the original endpoint's outcome type separate from the
// completed value advertised by MCP. Nested views retain Goa's declarations
// and cannot leak service-only fields into the completed result.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestInputExchangeResultLayoutsRetainEndpointOutcome(t *testing.T) {
	for _, mode := range []string{"ordinary", "fixed_view", "selected_views"} {
		t.Run(mode, func(t *testing.T) {
			root := codegen.RunDSL(t, func() {
				dsl.API("input_result", func() {})
				receipt := dsl.ResultType("application/vnd.input.receipt", func() {
					dsl.TypeName("Receipt")
					dsl.Field(1, "reference", dsl.String, "Receipt reference returned to the caller")
					dsl.Field(2, "internal", dsl.String, "Service-only receipt data")
					dsl.Required("reference", "internal")
					dsl.View("public", func() { dsl.Attribute("reference") })
					dsl.View("default", func() { dsl.Attribute("reference"); dsl.Attribute("internal") })
				})
				outcomeDSL := func() {
					dsl.OneOf("outcome", func() {
						dsl.Attribute("complete", receipt, "Completed receipt", func() { dsl.View("public") })
						dsl.Attribute("input_required", func() {
							dsl.Attribute("state", dsl.String, "Opaque operation state")
						})
					})
					dsl.Required("outcome")
				}
				var result expr.UserType
				if mode == "ordinary" {
					result = dsl.Type("OperationResult", outcomeDSL)
				} else {
					result = dsl.ResultType("application/vnd.input.operation", func() {
						dsl.TypeName("OperationResult")
						outcomeDSL()
						dsl.View("default", func() { dsl.Attribute("outcome") })
						if mode == "selected_views" {
							dsl.View("alternate", func() { dsl.Attribute("outcome") })
						}
					})
				}
				dsl.Service("receipts", func() {
					dsl.Method("book", func() {
						dsl.Payload(func() {
							dsl.Attribute("destination", dsl.String, "Requested destination")
							dsl.Attribute("continuation", func() { dsl.Attribute("state", dsl.String, "Returned operation state") })
							dsl.Required("destination")
						})
						dsl.Result(result)
						dsl.Meta(mcpinput.ExchangeMetaKey, "continuation", "outcome")
					})
				})
			})
			generation, err := codegen.NewGeneration("input.local/gen", []eval.Root{root})
			require.NoError(t, err)
			services, err := goaservice.NewPlan(root, generation, expr.NewExampleGenerator(root.API.RandomizerFactory))
			require.NoError(t, err)
			method := root.Services[0].Methods[0]
			if mode == "selected_views" {
				completed, _, err := planMCPResult(services, method)
				require.NoError(t, err)
				assert.NotNil(t, completed.Find("reference"))
				assert.Nil(t, completed.Find("outcome"))
			}
			endpoint, endpointLayout, err := planEndpointResult(services, method)
			require.NoError(t, err)
			assert.NotNil(t, endpoint.Find("outcome"))
			assert.NotNil(t, endpoint.Find("outcome").Type)
			assert.NotNil(t, endpointLayout.TypeDeclaration())
			for _, view := range []string{"default", "alternate"} {
				if mode != "selected_views" && view == "alternate" {
					continue
				}
				var completed *expr.AttributeExpr
				var completedLayout *codegen.GoTypePlan
				if mode == "selected_views" {
					completed, completedLayout, err = planMCPResultView(services, method, view)
				} else {
					completed, completedLayout, err = planMCPResult(services, method)
				}
				require.NoError(t, err)
				assert.NotNil(t, completed.Find("reference"))
				assert.Nil(t, completed.Find("internal"))
				assert.Nil(t, completed.Find("outcome"))
				assert.Equal(t, []string{"reference"}, completed.AllRequired())
				require.NotNil(t, completedLayout.TypeDeclaration())
				assert.NotSame(t, endpointLayout.TypeDeclaration(), completedLayout.TypeDeclaration())
				assert.NotNil(t, method.Result.Find("outcome"))
				assert.NotNil(t, expr.AsUnion(method.Result.Find("outcome").Type).Values[0].Attribute.Find("internal"))
			}
		})
	}
}
