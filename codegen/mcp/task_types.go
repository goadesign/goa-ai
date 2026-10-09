// Package codegen defines flat MCP Task replies with Goa's typed unions. The
// status discriminator selects exactly one working, input, result or error
// shape; generated servers and clients use the same contract.
package codegen

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// taskMetadataAttr builds the common fields on one observation. Retention is
// optional in Go and explicitly present in JSON, where null means unlimited.
func taskMetadataAttr() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "taskId", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Opaque identifier used for later requests to this task"}},
		{Name: "createdAt", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Creation time reported by the service"}},
		{Name: "lastUpdatedAt", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Time of this task observation reported by the service"}},
		{Name: "statusMessage", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Optional explanation of this task state"}},
		{Name: "ttlMs", Attribute: &expr.AttributeExpr{Type: expr.Int64, Description: "Retention from creation in integer milliseconds, or null for unlimited retention", Meta: expr.MetaExpr{"struct:tag:json": {"ttlMs"}}}},
		{Name: "pollIntervalMs", Attribute: &expr.AttributeExpr{Type: expr.Int64, Description: "Suggested interval before the next read in integer milliseconds"}},
	}, Validation: &expr.ValidationExpr{Required: []string{"taskId", "createdAt", "lastUpdatedAt"}}}
}

// buildTaskCreatedType adds the derived status to creation metadata. The
// enclosing tools/call union supplies resultType without a nested task object.
func (b *mcpExprBuilder) buildTaskCreatedType() *expr.AttributeExpr {
	attribute := taskMetadataAttr()
	object := expr.AsObject(attribute.Type)
	*object = append(*object,
		&expr.NamedAttributeExpr{Name: "status", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Current state derived from the service's observation", Validation: &expr.ValidationExpr{Values: []any{"working", inputRequiredBranch, "completed", "failed", "cancelled"}}}},
		&expr.NamedAttributeExpr{Name: "_meta", Attribute: protocolJSONAttribute("Namespaced protocol metadata and extension values")},
	)
	attribute.Validation.Required = append(attribute.Validation.Required, "status")
	return attribute
}

// buildTaskMethods adds reads and acknowledged updates for declared creators.
// Job state remains in the native service; these methods have no job storage.
func (b *mcpExprBuilder) buildTaskMethods() []*expr.MethodExpr {
	payload := func(name string, update bool) *expr.AttributeExpr {
		return b.userTypeAttr(name, func() *expr.AttributeExpr {
			attribute := &expr.AttributeExpr{Type: &expr.Object{{Name: "taskId", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Exact task identifier returned by tools/call"}}}, Validation: &expr.ValidationExpr{Required: []string{"taskId"}}}
			if update {
				*expr.AsObject(attribute.Type) = append(*expr.AsObject(attribute.Type), &expr.NamedAttributeExpr{Name: "inputResponses", Attribute: &expr.AttributeExpr{Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: protocolJSONAttribute("Host answer decoded through the declared question contract")}, Description: "Answers for any subset of the task's outstanding questions"}})
				attribute.Validation.Required = append(attribute.Validation.Required, "inputResponses")
			}
			return attribute
		})
	}
	return []*expr.MethodExpr{
		{Name: "tasks/get", Description: "Authorize a task read and return its full current observation", Payload: payload("TasksGetPayload", false), Result: b.taskDetailedResultAttr(), Errors: b.buildInputMethodErrors()},
		{Name: "tasks/update", Description: "Authorize and acknowledge submitted task answers; a later observation reports their effects", Payload: payload("TasksUpdatePayload", true), Result: b.userTypeAttr("TasksUpdateResult", func() *expr.AttributeExpr { return &expr.AttributeExpr{Type: &expr.Object{}} }), Errors: buildMCPMethodErrors(mcpDispatchErrors[:]...)},
		{Name: "tasks/cancel", Description: "Authorize and acknowledge cooperative cancellation; a later observation reports its effects", Payload: payload("TasksCancelPayload", false), Result: b.userTypeAttr("TasksCancelResult", func() *expr.AttributeExpr { return &expr.AttributeExpr{Type: &expr.Object{}} }), Errors: buildMCPMethodErrors(mcpDispatchErrors[:]...)},
	}
}

// taskDetailedResultAttr places the status-specific fields on the response body.
// A completed task carries the same finished tool result that synchronous calls
// return, while a failed task carries the service's explicit JSON-RPC error.
func (b *mcpExprBuilder) taskDetailedResultAttr() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: b.getOrCreateType("TasksGetResult", func() *expr.AttributeExpr {
		choice := &expr.Union{TypeName: "DetailedTask", TypeKey: "status", Flatten: true}
		for _, status := range []string{"working", inputRequiredBranch, "completed", "failed", "cancelled"} {
			name := "Task" + codegen.Goify(status, true) + "Result"
			branch := b.userTypeAttr(name, func() *expr.AttributeExpr {
				attribute := taskMetadataAttr()
				switch status {
				case inputRequiredBranch:
					input := b.buildInputRequiredResultType().Find("inputRequests")
					*expr.AsObject(attribute.Type) = append(*expr.AsObject(attribute.Type), &expr.NamedAttributeExpr{Name: "inputRequests", Attribute: expr.DupAtt(input)})
					attribute.Validation.Required = append(attribute.Validation.Required, "inputRequests")
				case "completed":
					finished := b.userTypeAttr("TaskToolResult", b.buildToolsCallResultType)
					*expr.AsObject(attribute.Type) = append(*expr.AsObject(attribute.Type), &expr.NamedAttributeExpr{Name: "result", Attribute: finished})
					attribute.Validation.Required = append(attribute.Validation.Required, "result")
				case "failed":
					failure := b.getOrCreateType("TaskError", func() *expr.AttributeExpr {
						return &expr.AttributeExpr{Type: &expr.Object{
							{Name: "code", Attribute: &expr.AttributeExpr{Type: expr.Int, Description: "JSON-RPC execution error code"}},
							{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "JSON-RPC execution error message"}},
							{Name: "data", Attribute: protocolJSONAttribute("Optional JSON-RPC execution error data")},
						}, Validation: &expr.ValidationExpr{Required: []string{"code", "message"}}}
					})
					*expr.AsObject(attribute.Type) = append(*expr.AsObject(attribute.Type), &expr.NamedAttributeExpr{Name: "error", Attribute: &expr.AttributeExpr{Type: failure, Description: "The explicit JSON-RPC error that stopped execution"}})
					attribute.Validation.Required = append(attribute.Validation.Required, "error")
				}
				return attribute
			})
			choice.Values = append(choice.Values, &expr.NamedAttributeExpr{Name: status, Attribute: branch})
		}
		return &expr.AttributeExpr{Type: &expr.Object{{Name: "outcome", Attribute: &expr.AttributeExpr{Type: choice, Description: "Exactly one full task observation"}}}, Validation: &expr.ValidationExpr{Required: []string{"outcome"}}}
	})}
}
