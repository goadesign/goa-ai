// Package mcp checks the authored stream that owns subscription selections.
// The service authorizes native resource and job identities and reports changes;
// generated code reads job snapshots through their configured methods, while the
// transport owns request correlation and notification ordering.
package mcp

import (
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// SubscriptionSourceExpr selects one authenticated change source.
	SubscriptionSourceExpr struct {
		eval.Expression
		// Method receives native selections and sends acknowledgment and changes.
		Method *expr.MethodExpr
	}
)

// EvalName identifies the subscription source in design errors.
func (s *SubscriptionSourceExpr) EvalName() string { return "MCP subscription source" }

// Validate requires one server stream whose accepted fields match its input.
// Resource events identify a URI; job events identify native jobs whose current
// state is read through their existing TaskExchange observation methods.
func (s *SubscriptionSourceExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if s.Method == nil {
		verr.Add(s, "subscription method is required")
		return verr
	}
	method := s.Method
	if method.Stream != expr.ServerStreamKind || method.HasMixedResults() {
		verr.Add(s, "subscription method must use only StreamingResult")
	}
	arguments, err := mcpinput.Arguments(method)
	if err != nil {
		verr.Add(s, "%s", err.Error())
		return verr
	}
	object := expr.AsObject(arguments.Type)
	if object == nil || len(*object) == 0 {
		verr.Add(s, "subscription input must select catalogs, resources or tasks")
		return verr
	}
	for _, field := range *object {
		switch field.Name {
		case resourceCatalogCollection:
			validateSubscriptionResources(verr, s, arguments)
		case "tasks":
			validateSubscriptionTasks(verr, s, arguments, nil)
		case "toolsListChanged", "promptsListChanged", "resourcesListChanged":
			validateSubscriptionCatalog(verr, s, arguments, field.Name)
			server := Root.GetMCP(method.Service)
			if server == nil || (field.Name == "toolsListChanged" && server.ToolCatalog == nil) || (field.Name == "promptsListChanged" && server.PromptCatalog == nil) || (field.Name == "resourcesListChanged" && server.ResourceCatalog == nil && server.ResourceTemplateCatalog == nil) {
				verr.Add(s, "%s requires an authored changing catalog", field.Name)
			}
		default:
			verr.Add(s, "subscription input has unsupported field %q", field.Name)
		}
	}
	result := method.StreamingResult
	if !hasValue(result) {
		verr.Add(s, "subscription stream must contain a required change OneOf")
		return verr
	}
	if _, viewed := result.Type.(*expr.ResultTypeExpr); viewed {
		verr.Add(s, "subscription events must not use result views")
	}
	event := expr.AsObject(result.Type)
	if event == nil || len(*event) != 1 || event.Attribute("change") == nil || !result.IsRequired("change") {
		verr.Add(s, "subscription stream must contain only a required change OneOf")
		return verr
	}
	choice := expr.AsUnion(event.Attribute("change").Type)
	if choice == nil {
		verr.Add(s, "subscription change must be a OneOf")
		return verr
	}
	expected := map[string]bool{"acknowledged": true}
	if object.Attribute(resourceCatalogCollection) != nil {
		expected["updated"] = true
	}
	if object.Attribute("tasks") != nil {
		expected["tasks_updated"] = true
	}
	for field, branch := range map[string]string{"toolsListChanged": "tools_changed", "promptsListChanged": "prompts_changed", "resourcesListChanged": "resources_changed"} {
		if object.Attribute(field) != nil {
			expected[branch] = true
		}
	}
	seen := make(map[string]bool, len(choice.Values))
	for _, branch := range choice.Values {
		if !expected[branch.Name] {
			verr.Add(s, "subscription change has unsupported branch %q", branch.Name)
			continue
		}
		seen[branch.Name] = true
		switch branch.Name {
		case "acknowledged":
			accepted := expr.AsObject(branch.Attribute.Type)
			if accepted == nil || len(*accepted) != len(*object) {
				verr.Add(s, "acknowledged selections must match the subscription input fields")
				continue
			}
			for _, field := range *object {
				if accepted.Attribute(field.Name) == nil {
					verr.Add(s, "acknowledged selections omit %q", field.Name)
					continue
				}
				switch field.Name {
				case resourceCatalogCollection:
					validateSubscriptionResources(verr, s, branch.Attribute)
				case "tasks":
					validateSubscriptionTasks(verr, s, branch.Attribute, field.Attribute)
				default:
					validateSubscriptionCatalog(verr, s, branch.Attribute, field.Name)
				}
			}
		case "tools_changed", "prompts_changed", "resources_changed":
			changed := expr.AsObject(branch.Attribute.Type)
			if changed == nil || len(*changed) != 0 {
				verr.Add(s, "%s must contain an empty object", branch.Name)
			}
		case "updated":
			update := expr.AsObject(branch.Attribute.Type)
			if update == nil || len(*update) != 1 || update.Attribute("uri") == nil || !branch.Attribute.IsRequired("uri") || !isPrimitive(update.Attribute("uri").Type, expr.String) {
				verr.Add(s, "updated branch must contain only a required uri string")
				continue
			}
			validation := expr.EffectiveValidation(update.Attribute("uri"))
			if validation == nil || validation.Format != expr.FormatURI {
				verr.Add(s, "updated uri must declare FormatURI")
			}
		case "tasks_updated":
			update := expr.AsObject(branch.Attribute.Type)
			if update == nil || len(*update) != 1 || update.Attribute("tasks") == nil || !branch.Attribute.IsRequired("tasks") {
				verr.Add(s, "tasks_updated must contain only a required tasks object")
				continue
			}
			validateSubscriptionTaskSelections(verr, s, update.Attribute("tasks"), object.Attribute("tasks"))
		}
	}
	for name := range expected {
		if !seen[name] {
			verr.Add(s, "subscription change must declare %q", name)
		}
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}

// validateSubscriptionResources checks the selected URI array. An empty accepted
// subset is valid, so the array remains optional and carries URI item validation.
func validateSubscriptionResources(verr *eval.ValidationErrors, source *SubscriptionSourceExpr, attribute *expr.AttributeExpr) {
	field := attribute.Find(resourceCatalogCollection)
	if field == nil {
		verr.Add(source, "subscription selections must contain optional resources")
		return
	}
	array := expr.AsArray(field.Type)
	if array == nil || !isPrimitive(array.ElemType.Type, expr.String) || attribute.IsRequired(resourceCatalogCollection) {
		verr.Add(source, "subscription resources must be an optional array of URI strings")
		return
	}
	validation := expr.EffectiveValidation(array.ElemType)
	if validation == nil || validation.Format != expr.FormatURI {
		verr.Add(source, "subscription resources elements must declare FormatURI")
	}
}

// validateSubscriptionTasks checks the optional native job selection object.
// Required job fields belong to individual update events, not the listen filter.
func validateSubscriptionTasks(verr *eval.ValidationErrors, source *SubscriptionSourceExpr, attribute, expected *expr.AttributeExpr) {
	field := attribute.Find("tasks")
	if field == nil || attribute.IsRequired("tasks") {
		verr.Add(source, "subscription tasks must be an optional object")
		return
	}
	validateSubscriptionTaskSelections(verr, source, field, expected)
}

// validateSubscriptionTaskSelections binds each selection name to a declared
// task creator. Arrays carry native string job identifiers; opaque MCP identities
// never enter the service's selection or change schema.
func validateSubscriptionTaskSelections(verr *eval.ValidationErrors, source *SubscriptionSourceExpr, attribute, expected *expr.AttributeExpr) {
	object := expr.AsObject(attribute.Type)
	if object == nil || len(*object) == 0 {
		verr.Add(source, "subscription tasks must contain creator-named job arrays")
		return
	}
	if expected != nil {
		original := expr.AsObject(expected.Type)
		if original == nil || len(*original) != len(*object) {
			verr.Add(source, "task selections must match the input's creator fields")
			return
		}
		for _, field := range *original {
			if object.Attribute(field.Name) == nil {
				verr.Add(source, "task selections omit creator %q", field.Name)
			}
		}
	}
	server := Root.GetMCP(source.Method.Service)
	for _, field := range *object {
		array := expr.AsArray(field.Attribute.Type)
		if array == nil || !isPrimitive(array.ElemType.Type, expr.String) || attribute.IsRequired(field.Name) {
			verr.Add(source, "task selection %q must be an optional array of native string job identifiers", field.Name)
			continue
		}
		creator := source.Method.Service.Method(field.Name)
		if creator == nil {
			verr.Add(source, "task selection %q names an unknown creator method", field.Name)
			continue
		}
		binding, err := mcpinput.TaskExchange(creator)
		if err != nil {
			verr.Add(source, "%s", err.Error())
			continue
		}
		bound := false
		if server != nil {
			for _, tool := range server.Tools {
				if tool.Method == creator {
					bound = true
					break
				}
			}
		}
		if binding == nil || !bound {
			verr.Add(source, "task selection %q must name a TaskExchange creator exposed as an MCP tool", field.Name)
		}
	}
}

// validateSubscriptionCatalog keeps acceptance explicit: absence or false does
// not select a catalog. Optional native aliases retain their generated layout.
func validateSubscriptionCatalog(verr *eval.ValidationErrors, source *SubscriptionSourceExpr, attribute *expr.AttributeExpr, field string) {
	value := attribute.Find(field)
	if value == nil || !isPrimitive(value.Type, expr.Boolean) || attribute.IsRequired(field) {
		verr.Add(source, "%s must be an optional boolean", field)
	}
}
