package agent

import "goa.design/goa/v3/eval"

// FederationExpr captures federation configuration for importing servers
// and agents from external registries.
type FederationExpr struct {
	eval.DSLFunc

	// Include specifies glob patterns for namespaces to import from
	// the federated source. If empty, all namespaces are included.
	Include []string
	// Exclude specifies glob patterns for namespaces to skip from
	// the federated source.
	Exclude []string
}

// EvalName implements eval.Expression allowing descriptive error messages.
func (f *FederationExpr) EvalName() string {
	return "federation configuration"
}
