// This file rejects MCP designs that the generated service cannot represent
// without changing or losing the user's service contract.

package codegen

import (
	"fmt"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/expr"
)

// validateMCPResources rejects methods that cannot represent one fixed MCP
// resource URI without hidden inputs.
func validateMCPResources(svc *expr.ServiceExpr, resources []*mcpexpr.ResourceExpr) error {
	for _, resource := range resources {
		arguments, err := mcpinput.Arguments(resource.Method)
		if err != nil {
			return err
		}
		if !hasMCPValue(arguments) || (expr.AsObject(arguments.Type) != nil && len(*expr.AsObject(arguments.Type)) == 0) {
			continue
		}
		return fmt.Errorf(
			`service %q resource method %q must not define a payload; fixed MCP resources have one readable URI`,
			svc.Name,
			resource.Method.Name,
		)
	}
	return nil
}
