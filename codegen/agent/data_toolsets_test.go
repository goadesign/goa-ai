package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goaexpr "goa.design/goa/v3/expr"
)

// TestNewToolDataRejectsMissingMethodSourceMetadata ensures method-backed tools
// fail at data construction time when the source service invariant is missing.
func TestNewToolDataRejectsMissingMethodSourceMetadata(t *testing.T) {
	tool, err := newToolData(
		&ToolsetData{
			Name:          "local",
			QualifiedName: "svc.local",
		},
		&agentsExpr.ToolExpr{
			Name:   "lookup",
			Method: &goaexpr.MethodExpr{Name: "Lookup"},
		},
		nil,
	)

	require.Nil(t, tool)
	require.ErrorContains(t, err, `method-backed tool "local.lookup" requires source service metadata`)
}
