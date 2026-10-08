// Package codegen connects an authored resource and job stream to an MCP listen
// request. Goa owns method types, authentication and middleware. The adapter
// fills native selections and validates events before the shared transport sends
// acknowledgment, resource updates or full job snapshots.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// subscriptionAdapter keeps source types and fields chosen by Goa.
	subscriptionAdapter struct {
		// Endpoint selects the configured original service method.
		Endpoint                                          *endpointMethodAdapter
		Tasks                                             []*subscriptionTaskSelection
		TaskInput                                         *jsoncodec.TransportField
		AcknowledgedTasks, UpdatedTasks, TasksUpdatedKind string
		// Catalogs retains only authored catalog selections and their event branches.
		Catalogs map[string]*subscriptionCatalogSelection
		// PayloadTransportRef names the private resource and job selection record.
		PayloadTransportRef string
		// PayloadConstructor applies authored input constraints and defaults.
		PayloadConstructor string
		// Resources retains the private URI array's field and element types.
		Resources *jsoncodec.TransportField
		// InputRef names Goa's streaming endpoint input record.
		InputRef string
		// SendName names the source interface's ordinary send method.
		SendName string
		// SendWithContextName names its send method with explicit cancellation.
		SendWithContextName string
		// MustClose records whether Goa's source interface requires Close.
		MustClose bool
		// ChangeField selects the authored event union.
		ChangeField string
		// AcknowledgedResources selects the accepted URI array.
		AcknowledgedResources string
		// UpdatedURIValue reads the changed address with Goa's pointer policy.
		UpdatedURIValue string
		// UnionRef names the original union without any named alias wrapper.
		UnionRef string
		// AcknowledgedKind names Goa's acknowledgment branch constant.
		AcknowledgedKind string
		// UpdatedKind names Goa's update branch constant.
		UpdatedKind string
		// Codec validates authored stream values before protocol conversion.
		Codec *MethodCodecData

		TaskSnapshotEncode  string
		TaskSnapshotPointer bool
		taskSnapshot        *jsoncodec.Value
		taskSnapshotPlan    *jsoncodec.Plan
		method              *expr.MethodExpr
		unionAttribute      *expr.AttributeExpr
		unionLayout         *codegen.GoTypePlan
	}

	// subscriptionCatalogSelection keeps one native flag beside its protocol event.
	subscriptionCatalogSelection struct {
		// Input fills the existing private constructor's typed selection field.
		Input *jsoncodec.TransportField
		// FilterField names the shared transport's boolean selection.
		FilterField string
		// AcceptedValue reads the native acknowledgment using Goa's field layout.
		AcceptedValue string
		// Kind selects the authored empty change branch.
		Kind string
		// Report names the shared transport operation that sends this change.
		Report string
	}
)

const subscriptionAcknowledgedBranch = "acknowledged"

// planSubscriptionSource reserves its private stream name before Goa freezes
// declarations and retains the original union attributes for later field lookup.
func planSubscriptionSource(generation *codegen.Generation, data *AdapterData) error {
	source := data.SubscriptionSource
	if source == nil {
		return nil
	}
	if err := generation.Package(data.mcpImportPath).DeclareName(codegen.NewExactName(codegen.NameType, "subscriptionStream")); err != nil {
		return err
	}
	choice := expr.AsObject(source.method.Result.Type).Attribute("change")
	source.unionAttribute = choice
	for {
		named, ok := source.unionAttribute.Type.(expr.UserType)
		if !ok {
			break
		}
		source.unionAttribute = named.Attribute()
	}
	matches := source.Endpoint.resultLayout.PlansForOccurrence(source.unionAttribute)
	if len(matches) != 1 || matches[0].UnionDeclaration() == nil {
		return fmt.Errorf("subscription change must have one generated union declaration")
	}
	source.unionLayout = matches[0]
	return planSubscriptionTasks(data)
}

// bindSubscriptionSource reads final constructors, fields and branch names.
// Goa's saved declaration supplies the original endpoint input record
// name, including any suffix chosen to avoid a declaration conflict.
func bindSubscriptionSource(generation *codegen.Generation, services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	source := data.SubscriptionSource
	if source == nil {
		return nil
	}
	method := services.Get(planned.prepared.userService.Name).Method(source.method.Name)
	stream := method.ServerStream
	source.SendName, source.SendWithContextName, source.MustClose = stream.SendName, stream.SendWithContextName, stream.MustClose
	// This empty attribute represents Goa's endpoint input record only for Go
	// name formatting. It is not a replacement for the service payload schema.
	record := &expr.AttributeExpr{
		Type: &expr.UserTypeExpr{
			TypeName:      "SubscriptionEndpointInput",
			AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{}},
		},
	}
	layout, err := codegen.PlanGoType(record, codegen.GoTypePlanOptions{
		Owner: data.serviceImportPath,
		Bind: func(_ codegen.GoTypeBindingRequest) (codegen.GoTypeBinding, error) {
			return codegen.GoTypeBinding{Owner: data.serviceImportPath, Declaration: method.EndpointInputDeclaration}, nil
		},
	})
	if err != nil {
		return err
	}
	source.InputRef = layout.Link(data.mcpImportPath, data.mcpPackage.ImportName).RefWithPointer(false)
	values := planned.methodCodecs[source.method.Name]
	source.Codec = methodCodecData(values, data.CodecPackage)
	source.PayloadTransportRef, err = values.payload.TransportTypeName(data.mcpImportPath, data.mcpPackage.ImportName)
	if err != nil {
		return err
	}
	source.PayloadConstructor = values.payload.TransportConstructorDeclaration().Name()
	resources := expr.AsObject(source.method.Payload.Type).Attribute("resources")
	if resources != nil {
		source.Resources, err = values.payload.TransportField(resources, "resources", data.mcpImportPath, data.mcpPackage.ImportName)
		if err != nil {
			return err
		}
	}
	scope := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
	source.ChangeField = scope.Field(expr.AsObject(source.method.Result.Type).Attribute("change"), "change", true)
	source.UnionRef = source.unionLayout.Link(data.mcpImportPath, data.mcpPackage.ImportName).RefWithPointer(false)
	owner := generation.Package(source.unionLayout.UnionDeclaration().PackagePath())
	for _, branch := range expr.AsUnion(source.unionAttribute.Type).Values {
		declaration, err := owner.UnionBranch(source.unionAttribute, branch.Name)
		if err != nil {
			return err
		}
		kind := data.mcpPackage.ImportName(owner.ImportPath()) + "." + declaration.KindConst()
		object := expr.AsObject(branch.Attribute.Type)
		switch branch.Name {
		case subscriptionAcknowledgedBranch:
			source.AcknowledgedKind = kind
			field := object.Attribute("resources")
			if field != nil {
				source.AcknowledgedResources = scope.Field(field, "resources", true)
			}
			if tasks := object.Attribute("tasks"); tasks != nil {
				source.AcknowledgedTasks = scope.Field(tasks, "tasks", true)
			}
		case "tasks_updated":
			source.TasksUpdatedKind = kind
			source.UpdatedTasks = scope.Field(object.Attribute("tasks"), "tasks", true)
		case "updated":
			source.UpdatedKind = kind
			field := object.Attribute("uri")
			selector := scope.Field(field, "uri", true)
			matches := source.Endpoint.resultLayout.PlansForOccurrence(field)
			if len(matches) != 1 {
				return fmt.Errorf("subscription updated URI must have one Go layout")
			}
			source.UpdatedURIValue = "selected." + selector
			if matches[0].IsPointer() {
				source.UpdatedURIValue = "*" + source.UpdatedURIValue
			}
		}
	}
	if err := bindSubscriptionCatalogs(generation, services, planned); err != nil {
		return err
	}
	return bindSubscriptionTasks(services, planned)
}

// buildSubscriptionAdapter requires the exact typed stream contract.
// Opaque Go substitutions cannot bypass the generated event validation.
func (g *adapterGenerator) buildSubscriptionAdapter() (*subscriptionAdapter, error) {
	declaration := g.mcp.SubscriptionSource
	if declaration == nil {
		return nil, nil
	}
	if err := declaration.Validate(); err != nil {
		return nil, err
	}
	for _, attribute := range []*expr.AttributeExpr{declaration.Method.Payload, declaration.Method.StreamingResult} {
		if err := checkContentGoType(attribute); err != nil {
			return nil, err
		}
	}
	return &subscriptionAdapter{method: declaration.Method}, nil
}

// bindSubscriptionCatalogs specializes only declared flags and event kinds.
// The shared transport checks the accepted subset and originating request; the
// generated source keeps native aliases, selectors and authentication intact.
func bindSubscriptionCatalogs(generation *codegen.Generation, services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	source := data.SubscriptionSource
	scope := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
	values := planned.methodCodecs[source.method.Name]
	var acknowledgment *expr.AttributeExpr
	for _, branch := range expr.AsUnion(source.unionAttribute.Type).Values {
		if branch.Name == subscriptionAcknowledgedBranch {
			acknowledgment = branch.Attribute
			break
		}
	}
	owner := generation.Package(source.unionLayout.UnionDeclaration().PackagePath())
	for _, selection := range []struct{ collection, field, filter, branch, report string }{
		{"tools", "toolsListChanged", "ToolsListChanged", "tools_changed", "ReportToolsChanged"},
		{"prompts", "promptsListChanged", "PromptsListChanged", "prompts_changed", "ReportPromptsChanged"},
		{"resources", "resourcesListChanged", "ResourcesListChanged", "resources_changed", "ReportResourcesChanged"},
	} {
		field := source.method.Payload.Find(selection.field)
		if field == nil {
			continue
		}
		input, err := values.payload.TransportField(field, selection.field, data.mcpImportPath, data.mcpPackage.ImportName)
		if err != nil {
			return err
		}
		accepted := acknowledgment.Find(selection.field)
		layouts := source.Endpoint.resultLayout.PlansForOccurrence(accepted)
		if len(layouts) != 1 {
			return fmt.Errorf("subscription catalog selection %q must have one Go layout", selection.field)
		}
		value := "selected." + scope.Field(accepted, selection.field, true)
		converted := "bool(" + value + ")"
		if layouts[0].IsPointer() {
			converted = value + " != nil && bool(*" + value + ")"
		}
		branch, err := owner.UnionBranch(source.unionAttribute, selection.branch)
		if err != nil {
			return err
		}
		if source.Catalogs == nil {
			source.Catalogs = make(map[string]*subscriptionCatalogSelection)
		}
		source.Catalogs[selection.collection] = &subscriptionCatalogSelection{
			Input: input, FilterField: selection.filter, AcceptedValue: converted,
			Kind: data.mcpPackage.ImportName(owner.ImportPath()) + "." + branch.KindConst(), Report: selection.report,
		}
	}
	return nil
}
