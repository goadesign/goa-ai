// Package codegen connects MCP task requests to existing configured Goa methods.
// A task ID carries the tool identity and the native job ID; the adapter stores
// no jobs or original arguments. The service owns authorization and job state.
package codegen

import (
	"encoding/base64"
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/inputexchange"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// taskOperation describes one protocol method while its dispatch is generated.
	taskOperation struct {
		Name, Wire, Result, Invoke string
		Read                       bool
	}
	// taskAdapter retains one tool's native observation and three task operations.
	taskAdapter struct {
		Tool                                                     *ToolAdapter
		ListenInputs                                             []*subscriptionReadInput
		Key                                                      string
		Prefix                                                   string
		Read, Answer, Cancel                                     *endpointMethodAdapter
		Input                                                    *inputexchange.TaskPlan
		TaskIDPointer, FailureCodePointer, FailureMessagePointer bool
		Created, Observed                                        *taskObservationAdapter
		Observations                                             []*taskObservationAdapter
		TaskIDField                                              string
		FailureCode, FailureMessage, FailureData                 string
		Roles                                                    []*taskRoleAdapter
		binding                                                  *mcpinput.TaskBinding
	}
	// taskObservationAdapter retains the types returned by one native endpoint.
	// Creation and reads may use different Goa view representations.
	taskObservationAdapter struct {
		Name, Validate, MetadataRef, MetadataField, OutcomeField string
		MetadataConversion                                       string
		MetadataHelpers                                          []*codegen.TransformFunctionData
		endpoint                                                 *endpointMethodAdapter
		attribute                                                *expr.AttributeExpr
		layout, metadataLayout                                   *codegen.GoTypePlan
		validation                                               *jsoncodec.Value
		metadataTransform                                        *codegen.TransformPlan
	}
	// taskRoleAdapter records the native identity field on one configured method.
	taskRoleAdapter struct {
		Endpoint                                *endpointMethodAdapter
		PayloadTransportRef, PayloadConstructor string
		TaskID, Responses                       *jsoncodec.TransportField
		// Defaults supplies authored domain values known before a later task request.
		Defaults []*jsoncodec.TransportField
		Answer   bool
	}
)

const (
	taskResponsesField = "responses"
	taskIdentityField  = "taskId"
)

// planTaskAdapters selects existing endpoints and verifies that every required
// input on a later request can come from its task ID, answers or native HTTP
// inputs. It rejects an unavailable input rather than saving creator arguments.
func planTaskAdapters(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	endpoints := make(map[*expr.MethodExpr]*endpointMethodAdapter, len(data.EndpointMethods))
	for _, endpoint := range data.EndpointMethods {
		endpoints[endpoint.method] = endpoint
	}
	pkg := generation.Package(data.mcpImportPath)
	for index, tool := range data.Tools {
		binding := prepared.tasks[tool.Name]
		if binding == nil {
			continue
		}
		task := &taskAdapter{Tool: tool, Key: base64.RawURLEncoding.EncodeToString([]byte(tool.Name)), Prefix: fmt.Sprintf("tool%dTask", index), binding: binding, Read: endpoints[binding.Read], Answer: endpoints[binding.Answer], Cancel: endpoints[binding.Cancel]}
		for roleIndex, endpoint := range []*endpointMethodAdapter{task.Read, task.Answer, task.Cancel} {
			known := map[string]bool{taskIdentityField: true}
			if roleIndex == 1 {
				known[taskResponsesField] = true
			}
			for _, field := range endpoint.Credentials {
				if field.Name == taskIdentityField || field.Name == taskResponsesField {
					return fmt.Errorf("MCP task method %q field %q cannot be both task data and a native HTTP input", endpoint.method.Name, field.Name)
				}
				known[field.Name] = true
			}
			for _, field := range endpoint.Paths {
				if field.Name == taskIdentityField || field.Name == taskResponsesField {
					return fmt.Errorf("MCP task method %q field %q cannot be both task data and a native HTTP input", endpoint.method.Name, field.Name)
				}
				known[field.Name] = true
			}
			arguments := expr.NewMappedAttributeExpr(endpoint.method.Payload)
			for _, field := range *expr.AsObject(endpoint.method.Payload.Type) {
				if endpoint.method.Payload.IsRequired(field.Name) && !known[field.Name] && arguments.GetDefault(field.Name) == nil {
					return fmt.Errorf("MCP task tool %q method %q requires field %q that tasks requests cannot supply; bind it as a native credential or route field, or derive it inside the service from taskId", tool.Name, endpoint.method.Name, field.Name)
				}
			}
			endpoint.TaskRole = true
			task.Roles = append(task.Roles, &taskRoleAdapter{Endpoint: endpoint, Answer: roleIndex == 1})
		}
		created := taskCreatedAttribute(prepared.mcpService.Method("tools/call").Result)
		creation := tool.Endpoint.resultAttribute
		input, err := mcpinput.InputExchange(binding.Creator)
		if err != nil {
			return err
		}
		if input != nil {
			for _, branch := range expr.AsUnion(creation.Find(input.OutcomeName).Type).Values {
				if branch.Name == completeBranch {
					creation = branch.Attribute
					break
				}
			}
		}
		task.Created = &taskObservationAdapter{Name: task.Prefix + "Created", endpoint: tool.Endpoint, attribute: creation}
		task.Observed = &taskObservationAdapter{Name: task.Prefix + "Observed", endpoint: task.Read, attribute: task.Read.resultAttribute}
		task.Observations = []*taskObservationAdapter{task.Created, task.Observed}
		for _, observation := range task.Observations {
			metadata := observation.attribute.Find("task")
			if metadata == nil || observation.attribute.Find("outcome") == nil {
				return fmt.Errorf("MCP task tool %q method %q result must retain task metadata and outcome", tool.Name, observation.endpoint.method.Name)
			}
			observation.layout, err = services.MethodTypeLayout(observation.endpoint.method, observation.attribute)
			if err != nil {
				return err
			}
			observation.metadataLayout, err = services.MethodTypeLayout(observation.endpoint.method, metadata)
			if err != nil {
				return err
			}
			observation.metadataTransform, err = codegen.NewTransformPlan(metadata, created, "taskMetadata", nil)
			if err != nil {
				return err
			}
			for index, helper := range observation.metadataTransform.Helpers() {
				declaration := codegen.NewExactName(codegen.NameFunction, fmt.Sprintf("%sMetadataHelper%d", observation.Name, index))
				if err := pkg.DeclareName(declaration); err != nil {
					return err
				}
				if err := observation.metadataTransform.BindHelperDeclaration(helper.ID, declaration); err != nil {
					return err
				}
			}
			if err := pkg.DeclareName(codegen.NewExactName(codegen.NameFunction, observation.Name+"Metadata")); err != nil {
				return err
			}
		}
		for _, suffix := range []string{"Input", "Answers", "Observation"} {
			if err := pkg.DeclareName(codegen.NewExactName(codegen.NameFunction, task.Prefix+suffix)); err != nil {
				return err
			}
		}
		tool.Endpoint.TaskCreator = true
		tool.Task = task
		data.Tasks = append(data.Tasks, task)
	}
	if len(data.Tasks) > 0 {
		for _, name := range []string{"decodeTaskID", "validateTaskCapabilities", "readTask", "answerTask", "cancelTask"} {
			if err := pkg.DeclareName(codegen.NewExactName(codegen.NameFunction, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// planTaskCodecs shares question conversion with native executors and validates
// the read method's complete observation before any status-specific reply.
func planTaskCodecs(services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData, codecs *jsoncodec.Plan) error {
	inputs := make(map[*expr.MethodExpr]*inputexchange.TaskPlan)
	for _, task := range data.Tasks {
		var err error
		task.Input = inputs[task.binding.Creator]
		if task.Input == nil {
			var pending *expr.AttributeExpr
			for _, branch := range expr.AsUnion(task.Read.resultAttribute.Find("outcome").Type).Values {
				if branch.Name == inputRequiredBranch {
					pending = expr.DupAtt(branch.Attribute)
					break
				}
			}
			if err := selectResultFields(pending, task.binding.Pending, make(map[resultTypePair]expr.DataType)); err != nil {
				return err
			}
			task.Input, err = inputexchange.NewTask(services, prepared.root.API, task.binding, pending, codecs)
			if err != nil {
				return err
			}
			inputs[task.binding.Creator] = task.Input
		}
		for _, observation := range task.Observations {
			observation.validation, err = codecs.Add(prepared.userService.Name+":"+task.Tool.Name+":"+observation.Name, observation.Name+"Observation", observation.attribute, observation.layout, jsoncodec.ValidateOnly)
			if err != nil {
				return err
			}
		}
		data.NeedsServerCodec = true
	}
	return nil
}

// bindTaskAdapters uses Goa's saved field names, pointer policy and metadata
// conversion. Only status and the opaque protocol identity are derived here.
func bindTaskAdapters(generation *codegen.Generation, services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	codec := services.ServiceAttributor(planned.prepared.userService.Name, data.CodecImportPath)
	native := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
	protocol := services.ServiceAttributor(planned.prepared.mcpService.Name, data.mcpImportPath)
	bound := make(map[*inputexchange.TaskPlan]bool)
	for _, task := range data.Tasks {
		source, writer := native, codec
		if task.Read.ExecutionView || task.Read.ProjectedResult {
			source = services.ViewAttributor(planned.prepared.userService.Name, data.mcpImportPath)
			writer = services.ViewAttributor(planned.prepared.userService.Name, data.CodecImportPath)
		}
		if !bound[task.Input] {
			if err := task.Input.BindCodecs(codec, writer); err != nil {
				return err
			}
			if err := task.Input.Bind(generation, native, data.mcpImportPath, data.mcpPackage.ImportName, data.CodecPackage); err != nil {
				return err
			}
			bound[task.Input] = true
		}
		for _, observation := range task.Observations {
			observer, validator := native, codec
			if observation.endpoint.ExecutionView || observation.endpoint.ProjectedResult {
				observer = services.ViewAttributor(planned.prepared.userService.Name, data.mcpImportPath)
				validator = services.ViewAttributor(planned.prepared.userService.Name, data.CodecImportPath)
			}
			if err := observation.validation.BindService(validator); err != nil {
				return err
			}
			observation.Validate = data.CodecPackage + "." + observation.validation.ValidationDeclaration().Name()
			metadata := observation.attribute.Find("task")
			observation.MetadataField = observer.Field(metadata, "task", true)
			observation.OutcomeField = observer.Field(observation.attribute.Find("outcome"), "outcome", true)
			observation.MetadataRef = observation.metadataLayout.Link(data.mcpImportPath, data.mcpPackage.ImportName).Ref()
			if err := observation.metadataTransform.BindContexts(&codegen.AttributeContext{Scope: observer, UseDefault: true, Pointer: observation.metadataLayout.Policy().Pointer}, &codegen.AttributeContext{Scope: protocol, UseDefault: true}); err != nil {
				return err
			}
			var err error
			observation.MetadataConversion, observation.MetadataHelpers, err = observation.metadataTransform.Render("metadata", "out", true)
			if err != nil {
				return err
			}
		}
		metadata := task.Observed.attribute.Find("task")
		task.TaskIDField = source.Field(metadata.Find(taskIdentityField), taskIdentityField, true)
		identity := task.Observed.metadataLayout.PlansForOccurrence(metadata.Find(taskIdentityField))
		if len(identity) != 1 {
			return fmt.Errorf("task metadata identity must have one Go layout")
		}
		task.TaskIDPointer = identity[0].IsPointer()
		for _, branch := range expr.AsUnion(task.Read.resultAttribute.Find("outcome").Type).Values {
			if branch.Name != "failed" {
				continue
			}
			task.FailureCode = source.Field(branch.Attribute.Find("code"), "code", true)
			task.FailureMessage = source.Field(branch.Attribute.Find("message"), "message", true)
			for _, field := range []struct {
				name   string
				target *bool
			}{{"code", &task.FailureCodePointer}, {"message", &task.FailureMessagePointer}} {
				fields := task.Read.resultLayout.PlansForOccurrence(branch.Attribute.Find(field.name))
				if len(fields) != 1 {
					return fmt.Errorf("task failure field %q must have one Go layout", field.name)
				}
				*field.target = fields[0].IsPointer()
			}
			if field := branch.Attribute.Find("data"); field != nil {
				task.FailureData = source.Field(field, "data", true)
			}
		}
		var err error
		for _, role := range task.Roles {
			values := planned.methodCodecs[role.Endpoint.method.Name]
			role.PayloadTransportRef, err = values.payload.TransportTypeName(data.mcpImportPath, data.mcpPackage.ImportName)
			if err != nil {
				return err
			}
			role.PayloadConstructor = values.payload.TransportConstructorDeclaration().Name()
			arguments, err := mcpinput.Arguments(role.Endpoint.method)
			if err != nil {
				return err
			}
			for _, field := range *expr.AsObject(arguments.Type) {
				if field.Name == taskIdentityField || field.Name == taskResponsesField {
					continue
				}
				planned, err := values.payload.TransportField(field.Attribute, field.Name, data.mcpImportPath, data.mcpPackage.ImportName)
				if err != nil {
					return err
				}
				if planned.Default != nil {
					role.Defaults = append(role.Defaults, planned)
				}
			}
			identity := role.Endpoint.method.Payload.Find(taskIdentityField)
			role.TaskID, err = values.payload.TransportField(identity, taskIdentityField, data.mcpImportPath, data.mcpPackage.ImportName)
			if err != nil {
				return err
			}
			if role.Answer {
				responses := role.Endpoint.method.Payload.Find(taskResponsesField)
				role.Responses, err = values.payload.TransportField(responses, taskResponsesField, data.mcpImportPath, data.mcpPackage.ImportName)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// taskCreatedAttribute selects the protocol creation branch used by metadata
// conversion. It is defined for all tools/call clients, independently of whether
// this server binds a creator, so mixed services share one protocol type.
func taskCreatedAttribute(result *expr.AttributeExpr) *expr.AttributeExpr {
	for _, branch := range expr.AsUnion(result.Find("outcome").Type).Values {
		if branch.Name == "task" {
			return branch.Attribute
		}
	}
	panic("tools/call has no Task creation branch")
}

// taskOperations supplies the three fixed operations to the template. Runtime
// code receives one generated method for each operation, without dispatch modes.
func taskOperations() []taskOperation {
	return []taskOperation{
		{Name: "TasksGet", Wire: "tasks/get", Result: "TasksGetResult", Invoke: "readTask", Read: true},
		{Name: "TasksUpdate", Wire: "tasks/update", Result: "TasksUpdateResult", Invoke: "answerTask"},
		{Name: "TasksCancel", Wire: "tasks/cancel", Result: "TasksCancelResult", Invoke: "cancelTask"},
	}
}
