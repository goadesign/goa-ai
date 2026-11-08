package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/internal/mcpinput"
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

// TestHasDeclaredErrorTool verifies that import planning gates planner and
// toolregistry imports on eligible non-fault, non-timeout declared errors and
// excludes task-backed methods.
func TestHasDeclaredErrorTool(t *testing.T) {
	t.Parallel()

	eligibleErr := &goaexpr.ErrorExpr{
		Name:          "invalid_state",
		AttributeExpr: &goaexpr.AttributeExpr{},
	}
	faultErr := &goaexpr.ErrorExpr{
		Name: "internal_crash",
		AttributeExpr: &goaexpr.AttributeExpr{
			Meta: goaexpr.MetaExpr{"goa:error:fault": nil},
		},
	}
	timeoutErr := &goaexpr.ErrorExpr{
		Name: "slow_upstream",
		AttributeExpr: &goaexpr.AttributeExpr{
			Meta: goaexpr.MetaExpr{"goa:error:timeout": nil},
		},
	}

	assert.True(t, hasDeclaredErrorTool([]*agentsExpr.ToolExpr{
		{
			Name: "update",
			Method: &goaexpr.MethodExpr{
				Name:   "Update",
				Errors: []*goaexpr.ErrorExpr{eligibleErr},
			},
		},
	}))

	assert.False(t, hasDeclaredErrorTool([]*agentsExpr.ToolExpr{
		{
			Name: "update",
			Method: &goaexpr.MethodExpr{
				Name:   "Update",
				Errors: []*goaexpr.ErrorExpr{faultErr, timeoutErr},
			},
		},
	}), "Fault() and Timeout() design errors must not trigger declared-error imports")

	assert.False(t, hasDeclaredErrorTool([]*agentsExpr.ToolExpr{
		{
			Name: "create_task",
			Method: &goaexpr.MethodExpr{
				Name:   "CreateTask",
				Errors: []*goaexpr.ErrorExpr{eligibleErr},
				Meta: goaexpr.MetaExpr{
					mcpinput.TaskExchangeMetaKey: {"read", "answer", "cancel"},
				},
			},
		},
	}), "task-backed methods must be excluded from declared-error mapping")
}
