package agent

import (
	"fmt"

	"goa.design/goa/v3/eval"
)

// A2AExpr captures A2A configuration for an exported toolset.
// It is attached to ToolsetExpr instances that are marked as A2A-exposed
// via the A2A DSL helper.
type A2AExpr struct {
	eval.DSLFunc

	// Suite is the A2A suite identifier for the exported toolset.
	// When not explicitly set in the DSL, it defaults to
	// "<service>.<agent>.<toolset>" for agent-level exports.
	Suite string

	// Path is the HTTP path where the A2A JSON-RPC endpoint is exposed.
	// When not explicitly set in the DSL, it defaults to "/a2a".
	Path string

	// Version is the A2A protocol version supported by this server.
	// When not explicitly set in the DSL, it defaults to "1.0".
	Version string
}

// EvalName implements eval.Expression and provides a descriptive name for
// error reporting.
func (a *A2AExpr) EvalName() string {
	return fmt.Sprintf("A2A configuration (suite=%q, path=%q, version=%q)", a.Suite, a.Path, a.Version)
}

// Validate performs local validation on the A2A configuration. Cross-toolset
// invariants (such as ensuring A2A is only present on exported toolsets and
// applying defaults based on service/agent names) are enforced at the root
// level in RootExpr.Validate.
func (a *A2AExpr) Validate() error {
	verr := new(eval.ValidationErrors)

	// Path, when set, must be an absolute path.
	if a.Path != "" && a.Path[0] != '/' {
		verr.Add(a, "A2A path must start with '/' when set; got %q", a.Path)
	}

	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}


