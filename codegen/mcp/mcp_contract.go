// This file rejects MCP designs that the generated service cannot represent
// without changing or losing the user's service contract.

package codegen

import (
	"fmt"
	"sort"
	"strings"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

// validateMCPService accepts only methods the generated MCP adapter can
// represent without losing information.
func validateMCPService(svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr) error {
	if err := validateMCPResources(svc, mcp.Resources); err != nil {
		return err
	}
	mapped := make(map[string]struct{}, len(svc.Methods))
	for _, tool := range mcp.Tools {
		mapped[tool.Method.Name] = struct{}{}
	}
	for _, resource := range mcp.Resources {
		mapped[resource.Method.Name] = struct{}{}
	}
	unmapped := make([]string, 0, len(svc.Methods))
	for _, method := range svc.Methods {
		if _, ok := mapped[method.Name]; ok {
			continue
		}
		unmapped = append(unmapped, method.Name)
	}
	if len(unmapped) == 0 {
		return nil
	}

	sort.Strings(unmapped)
	return fmt.Errorf(
		`service %q has methods not mapped to MCP constructs: %s`,
		svc.Name,
		strings.Join(unmapped, ", "),
	)
}

// validateMCPResources rejects methods that cannot represent one fixed MCP
// resource URI without hidden inputs.
func validateMCPResources(svc *expr.ServiceExpr, resources []*mcpexpr.ResourceExpr) error {
	for _, resource := range resources {
		if resource.Method.Payload == nil || resource.Method.Payload.Type == expr.Empty {
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
