// Package codegen translates a listen request's opaque task IDs into the
// source method's typed native job selections. Each stream retains only its
// requested and accepted IDs; updates reuse the configured task read endpoint.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// subscriptionTaskSelection connects one creator's native selection array to
	// all MCP tools bound to that creator, including their independently named IDs.
	subscriptionTaskSelection struct {
		Name, Prefix, AcceptedField, UpdatedField string
		Transport                                 *jsoncodec.TransportField
		Tools                                     []*taskAdapter
		attribute                                 *expr.AttributeExpr
	}
	// subscriptionReadInput copies one already-decoded native HTTP input between
	// protocol payloads. Names come from the existing credential and URL planners.
	subscriptionReadInput struct{ Target, Source string }
)

// planSubscriptionTasks selects only the creator fields in the authored source.
// Several MCP tool names can share a creator without entering its native schema.
func planSubscriptionTasks(data *AdapterData) error {
	source := data.SubscriptionSource
	tasks := source.method.Payload.Find("tasks")
	if tasks == nil {
		return nil
	}
	for index, field := range *expr.AsObject(tasks.Type) {
		selection := &subscriptionTaskSelection{Name: field.Name, Prefix: fmt.Sprintf("task%d", index), attribute: field.Attribute}
		for _, task := range data.Tasks {
			if task.binding.Creator.Name == field.Name {
				selection.Tools = append(selection.Tools, task)
			}
		}
		if len(selection.Tools) == 0 {
			return fmt.Errorf("subscription creator %q has no planned MCP task tool", field.Name)
		}
		source.Tasks = append(source.Tasks, selection)
	}
	return nil
}

// bindSubscriptionTasks retains exact private input types and native selectors.
// A notification invokes the same read method with the same HTTP inputs as a
// tasks/get request; no source code reconstructs credentials or result views.
func bindSubscriptionTasks(services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	source := data.SubscriptionSource
	if len(source.Tasks) == 0 {
		return nil
	}
	payload := planned.methodCodecs[source.method.Name].payload
	scope := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
	var err error
	source.TaskInput, err = payload.TransportField(source.method.Payload.Find("tasks"), "tasks", data.mcpImportPath, data.mcpPackage.ImportName)
	if err != nil {
		return err
	}
	choice := expr.AsUnion(source.unionAttribute.Type)
	var accepted, updated *expr.AttributeExpr
	for _, branch := range choice.Values {
		if branch.Name == "acknowledged" {
			accepted = branch.Attribute.Find("tasks")
		}
		if branch.Name == "tasks_updated" {
			updated = branch.Attribute.Find("tasks")
		}
	}
	for _, selection := range source.Tasks {
		selection.Transport, err = payload.TransportField(selection.attribute, selection.Name, data.mcpImportPath, data.mcpPackage.ImportName)
		if err != nil {
			return err
		}
		selection.AcceptedField = scope.Field(accepted.Find(selection.Name), selection.Name, true)
		selection.UpdatedField = scope.Field(updated.Find(selection.Name), selection.Name, true)
		for _, task := range selection.Tools {
			for _, credential := range task.Read.Credentials {
				task.ListenInputs = append(task.ListenInputs, &subscriptionReadInput{Target: credential.Sources["tasks/get"], Source: credential.Sources["subscriptions/listen"]})
			}
			for _, field := range task.Read.Paths {
				task.ListenInputs = append(task.ListenInputs, &subscriptionReadInput{Target: field.Sources["tasks/get"], Source: field.Sources["subscriptions/listen"]})
			}
		}
	}
	return nil
}
