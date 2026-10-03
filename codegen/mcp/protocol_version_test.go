// These tests verify that the generated protocol is owned by the release,
// while the authored server name and software version remain ordinary identity.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

func TestMCPMethodsUseCurrentRequestMetadata(t *testing.T) {
	svc, _ := testService("calc")
	builder := newMCPExprBuilder(svc, &mcpexpr.MCPExpr{Name: "calc", Version: "1"})
	methods := builder.buildMethods()
	require.Len(t, methods, 1)
	assert.Equal(t, "server/discover", methods[0].Name)
	assert.True(t, methods[0].Payload.IsRequired("_meta"))
	assert.NotNil(t, expr.AsObject(methods[0].Payload.Type).Attribute("_meta"))
	result := expr.AsObject(methods[0].Result.Type)
	assert.NotNil(t, result.Attribute("resultType"))
	assert.NotNil(t, result.Attribute("supportedVersions"))
	assert.NotNil(t, result.Attribute("ttlMs"))
	assert.NotNil(t, result.Attribute("cacheScope"))
}
