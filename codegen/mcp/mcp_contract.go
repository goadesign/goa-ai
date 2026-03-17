package codegen

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

const missingJSONRPCRouteMessage = `service %q must declare JSONRPC(func(){ POST(...) }) with a service-level path`

type (
	notificationPayloadField struct {
		Attribute *expr.AttributeExpr
		Required  bool
	}
)

// validatePureMCPService enforces the generator contract that MCP-enabled
// services are MCP-only, preserve the source JSON-RPC transport shape, and use
// notification payloads the generated MCP notification adapter can represent
// without lossy coercion.
func validatePureMCPService(svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr, source *sourceSnapshot) error {
	if err := rejectUnsupportedPureMCPMethods(svc, mcp); err != nil {
		return err
	}
	if err := validatePureMCPJSONRPCRoute(svc, source); err != nil {
		return err
	}
	if err := validatePureMCPResources(svc, mcp.Resources); err != nil {
		return err
	}
	if err := validatePureMCPNotifications(svc, mcp.Notifications); err != nil {
		return err
	}

	mapped := make(map[string]struct{}, len(svc.Methods))
	for _, tool := range mcp.Tools {
		mapped[tool.Method.Name] = struct{}{}
	}
	for _, resource := range mcp.Resources {
		mapped[resource.Method.Name] = struct{}{}
	}
	for _, notification := range mcp.Notifications {
		mapped[notification.Method.Name] = struct{}{}
	}
	if mcpexpr.Root != nil {
		for _, prompt := range mcpexpr.Root.DynamicPrompts[svc.Name] {
			mapped[prompt.Method.Name] = struct{}{}
		}
	}

	var unmapped []string
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

// validatePureMCPJSONRPCRoute ensures the generated MCP transport reuses the
// original service-level JSON-RPC route shape instead of silently changing it.
func validatePureMCPJSONRPCRoute(svc *expr.ServiceExpr, source *sourceSnapshot) error {
	route, ok := source.jsonrpcRoute(svc.Name)
	if !ok || route.path == "" {
		return fmt.Errorf(missingJSONRPCRouteMessage, svc.Name)
	}
	if route.method != http.MethodPost {
		return fmt.Errorf(
			missingJSONRPCRouteMessage+`; found %s %q`,
			svc.Name,
			route.method,
			route.path,
		)
	}
	return nil
}

// validatePureMCPResources rejects resource payloads the adapter cannot map to
// deterministic URI query parameters without generic runtime coercion.
func validatePureMCPResources(svc *expr.ServiceExpr, resources []*mcpexpr.ResourceExpr) error {
	for _, resource := range resources {
		if resource.Method.Payload == nil || resource.Method.Payload.Type == expr.Empty {
			continue
		}
		if _, err := buildResourceQueryFields(resource.Method.Payload); err != nil {
			return fmt.Errorf(
				`service %q resource method %q has incompatible resource query payload: %w`,
				svc.Name,
				resource.Method.Name,
				err,
			)
		}
	}
	return nil
}

// validatePureMCPNotifications rejects original notification method payloads
// that cannot be losslessly expressed by the generated SendNotificationPayload
// contract.
func validatePureMCPNotifications(svc *expr.ServiceExpr, notifications []*mcpexpr.NotificationExpr) error {
	for _, notification := range notifications {
		if notification.Method.Result != nil && notification.Method.Result.Type != expr.Empty {
			return fmt.Errorf(
				`service %q notification method %q must not declare a result`,
				svc.Name,
				notification.Method.Name,
			)
		}
		if err := validatePureMCPNotificationPayload(notification.Method); err != nil {
			return fmt.Errorf(
				`service %q notification method %q has incompatible notification payload: %w`,
				svc.Name,
				notification.Method.Name,
				err,
			)
		}
	}
	return nil
}

// validatePureMCPNotificationPayload accepts only the exact top-level fields the
// generated MCP notification method can forward without dropping information.
func validatePureMCPNotificationPayload(method *expr.MethodExpr) error {
	payload := method.Payload
	fields := collectNotificationPayloadFields(payload)
	if len(fields) == 0 {
		return fmt.Errorf(`must define a payload object with required "type" string field`)
	}
	for name := range fields {
		switch name {
		case "type", "message", "data":
		default:
			return fmt.Errorf(`field %q is not supported; expected only "type", "message", and optional "data"`, name)
		}
	}
	typeField, ok := fields["type"]
	if !ok {
		return fmt.Errorf(`must define required "type" string field`)
	}
	if !typeField.Required {
		return fmt.Errorf(`field "type" must be required`)
	}
	if !isNotificationStringField(typeField.Attribute) {
		return fmt.Errorf(`field "type" must be a string`)
	}
	if messageField, ok := fields["message"]; ok && !isNotificationStringField(messageField.Attribute) {
		return fmt.Errorf(`field "message" must be a string`)
	}
	return nil
}

// collectNotificationPayloadFields flattens the top-level notification payload
// schema across direct fields, bases, and references so contract checks do not
// accidentally ignore inherited attributes.
func collectNotificationPayloadFields(payload *expr.AttributeExpr) map[string]notificationPayloadField {
	fields := make(map[string]notificationPayloadField)
	collectNotificationAttributeFields(payload, payload, fields, make(map[string]struct{}))
	return fields
}

// collectNotificationAttributeFields walks one attribute graph exactly once per
// underlying type hash to keep recursive user types from looping forever.
func collectNotificationAttributeFields(
	root *expr.AttributeExpr,
	att *expr.AttributeExpr,
	fields map[string]notificationPayloadField,
	seen map[string]struct{},
) {
	if att == nil || att.Type == nil {
		return
	}
	hash := att.Type.Hash()
	if _, ok := seen[hash]; ok {
		return
	}
	seen[hash] = struct{}{}
	for _, base := range att.Bases {
		collectNotificationAttributeFields(root, attributeDataType(base), fields, seen)
	}
	for _, ref := range att.References {
		collectNotificationAttributeFields(root, attributeDataType(ref), fields, seen)
	}
	if object := expr.AsObject(att.Type); object != nil {
		for _, nat := range *object {
			field := notificationPayloadField{
				Attribute: nat.Attribute,
				Required:  root.IsRequired(nat.Name) || att.IsRequired(nat.Name),
			}
			if existing, ok := fields[nat.Name]; ok && existing.Required {
				field.Required = true
			}
			fields[nat.Name] = field
		}
	}
}

// isNotificationStringField accepts string aliases as well as bare expr.String.
func isNotificationStringField(att *expr.AttributeExpr) bool {
	return unwrapNotificationFieldType(att.Type) == expr.String
}

// unwrapNotificationFieldType resolves user and result type wrappers to their
// underlying data type so primitive compatibility checks stay alias-aware.
func unwrapNotificationFieldType(dt expr.DataType) expr.DataType {
	switch actual := dt.(type) {
	case *expr.UserTypeExpr:
		return unwrapNotificationFieldType(actual.Type)
	case *expr.ResultTypeExpr:
		return unwrapNotificationFieldType(actual.Type)
	default:
		return actual
	}
}

// rejectUnsupportedPureMCPMethods fails fast when the MCP DSL includes
// constructs the original-service client adapter cannot expose yet.
func rejectUnsupportedPureMCPMethods(svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr) error {
	if err := rejectUnsupportedPureMCPSubscriptions(svc, mcp.Subscriptions); err != nil {
		return err
	}
	return rejectUnsupportedPureMCPMonitorMethods(svc, mcp.SubscriptionMonitors)
}

// rejectUnsupportedPureMCPSubscriptions reports unsupported subscription
// mappings using the original service method names so users can fix the DSL.
func rejectUnsupportedPureMCPSubscriptions(svc *expr.ServiceExpr, subscriptions []*mcpexpr.SubscriptionExpr) error {
	if len(subscriptions) == 0 {
		return nil
	}

	names := make([]string, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		names = append(names, subscription.Method.Name)
	}
	sort.Strings(names)
	return fmt.Errorf(
		`service %q maps methods to unsupported MCP subscriptions: %s`,
		svc.Name,
		strings.Join(names, ", "),
	)
}

// rejectUnsupportedPureMCPMonitorMethods handles monitors separately because the
// Goa expression type does not share a common interface with subscriptions.
func rejectUnsupportedPureMCPMonitorMethods(svc *expr.ServiceExpr, monitors []*mcpexpr.SubscriptionMonitorExpr) error {
	if len(monitors) == 0 {
		return nil
	}

	names := make([]string, 0, len(monitors))
	for _, monitor := range monitors {
		names = append(names, monitor.Method.Name)
	}
	sort.Strings(names)
	return fmt.Errorf(
		`service %q maps methods to unsupported MCP subscription monitors: %s`,
		svc.Name,
		strings.Join(names, ", "),
	)
}
