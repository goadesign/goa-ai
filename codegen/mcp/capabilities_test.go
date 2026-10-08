// These checks model the shared protocol types used by several MCP services in
// one generation run. Their layout must not depend on which service is first.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

func TestMCPSharedResourceCapabilityLayout(t *testing.T) {
	for _, subscriptionFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary first", true: "subscription first"}[subscriptionFirst], func(t *testing.T) {
			first := newMCPExprBuilder(&expr.ServiceExpr{Name: "first"}, capabilitySource(subscriptionFirst))
			first.buildMCPTypes()
			second := newMCPExprBuilder(&expr.ServiceExpr{Name: "second"}, capabilitySource(!subscriptionFirst))
			for name, attribute := range first.Types() {
				second.Types()[name] = attribute
			}
			second.buildMCPTypes()
			resources := second.Types()["ResourcesCapability"].Attribute()
			assert.NotNil(t, resources.Find("subscribe"))
			assert.False(t, resources.IsRequired("subscribe"))
		})
	}
}

// capabilitySource supplies only the selection that determines resource
// subscription capability; stream validation has its own authored DSL checks.
func capabilitySource(enabled bool) *mcpexpr.MCPExpr {
	definition := &mcpexpr.MCPExpr{}
	if enabled {
		definition.SubscriptionSource = &mcpexpr.SubscriptionSourceExpr{Method: &expr.MethodExpr{Payload: &expr.AttributeExpr{Type: &expr.Object{{Name: "resources", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}}}}}}}}
	}
	return definition
}
