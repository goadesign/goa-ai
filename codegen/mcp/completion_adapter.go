// Package codegen binds prompt and resource completion methods to generated payload
// constructors and result conversions. Names and field types are decided during
// generation; runtime code only selects the requested reference and argument.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// completionReferenceAdapter contains argument names known during generation.
	completionReferenceAdapter struct {
		// Type selects the reference shape supplied by the client.
		Type string
		// Name is the prompt name or exact URI template.
		Name string
		// Arguments is the complete declared variable set for this reference.
		Arguments []string
	}
	// completionAdapter retains the one method selected by a DSL binding.
	completionAdapter struct {
		// ReferenceType selects a prompt or resource reference.
		ReferenceType string
		// Reference is the exact declared prompt name or URI template.
		Reference string
		// Argument is the declared argument being completed.
		Argument string
		// Endpoint calls the configured Goa endpoint for this method.
		Endpoint *endpointMethodAdapter
		// PayloadTransportRef is the private decoded-input type filled by the adapter.
		PayloadTransportRef string
		// PayloadConstructor is the generated constructor that applies defaults and validation.
		PayloadConstructor string
		// Value is the private field storing the partial input text.
		Value *jsoncodec.TransportField
		// Arguments is the private map field storing prior argument values.
		Arguments *jsoncodec.TransportField
		// Codec is the typed result validator selected for the service method.
		Codec *MethodCodecData
		// Conversion is the generated copy from the service result to MCP suggestions.
		Conversion string
		// Helpers contains the typed conversion functions used by that copy.
		Helpers []*codegen.TransformFunctionData

		method       *expr.MethodExpr
		transform    *codegen.TransformPlan
		resultLayout *codegen.GoTypePlan
	}
)

// planCompletionConversions registers complete result imports and transform
// helper names before Goa freezes names. The target is the real protocol type.
func planCompletionConversions(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	if len(data.Completions) == 0 {
		return nil
	}
	target := expr.AsObject(prepared.mcpService.Method("completion/complete").Result.Type).Attribute("completion")
	pkg := generation.Package(data.mcpImportPath)
	imports := codegen.NewGeneratedImportPlan(pkg)
	for index, completion := range data.Completions {
		result, layout, err := planMCPResult(services, completion.method)
		if err != nil {
			return err
		}
		if expr.AsObject(result.Type).Attribute("values") == nil {
			return fmt.Errorf("completion method %q selected view omits values", completion.method.Name)
		}
		if err := imports.AddCompleteType(layout); err != nil {
			return err
		}
		transform, err := codegen.NewTransformPlan(result, target, "completion", nil)
		if err != nil {
			return err
		}
		for helperIndex, helper := range transform.Helpers() {
			declaration := codegen.NewExactName(codegen.NameFunction, fmt.Sprintf("convertCompletion%dHelper%d", index, helperIndex))
			if err := pkg.DeclareName(declaration); err != nil {
				return err
			}
			if err := transform.BindHelperDeclaration(helper.ID, declaration); err != nil {
				return err
			}
		}
		completion.transform, completion.resultLayout = transform, layout
	}
	for _, importPath := range imports.Paths() {
		if importPath != data.mcpImportPath {
			data.serverImportPaths = append(data.serverImportPaths, importPath)
		}
	}
	return nil
}

// bindCompletionConversions fills private constructor fields and renders the
// typed result copy after Goa has assigned final service and field names.
func bindCompletionConversions(services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	targetScope := services.ServiceAttributor(planned.prepared.mcpService.Name, data.mcpImportPath)
	target := &codegen.AttributeContext{Scope: targetScope, UseDefault: true}
	for _, completion := range data.Completions {
		method := completion.method
		sourceScope := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
		if completion.Endpoint.ProjectedResult || completion.Endpoint.ExecutionView {
			sourceScope = services.ViewAttributor(planned.prepared.userService.Name, data.mcpImportPath)
		}
		source := &codegen.AttributeContext{Scope: sourceScope, UseDefault: true, Pointer: completion.resultLayout.Policy().Pointer}
		values := planned.methodCodecs[method.Name]
		completion.Codec = methodCodecData(values, data.CodecPackage)
		transport, err := values.payload.TransportTypeName(data.mcpImportPath, data.mcpPackage.ImportName)
		if err != nil {
			return err
		}
		completion.PayloadTransportRef = transport
		completion.PayloadConstructor = values.payload.TransportConstructorDeclaration().Name()
		object := expr.AsObject(method.Payload.Type)
		completion.Value, err = values.payload.TransportField(object.Attribute("value"), "value", data.mcpImportPath, data.mcpPackage.ImportName)
		if err != nil {
			return err
		}
		completion.Arguments, err = values.payload.TransportField(object.Attribute("arguments"), "arguments", data.mcpImportPath, data.mcpPackage.ImportName)
		if err != nil {
			return err
		}
		if err := completion.transform.BindContexts(source, target); err != nil {
			return err
		}
		completion.Conversion, completion.Helpers, err = completion.transform.Render(completion.Endpoint.ResultValue, "out", true)
		if err != nil {
			return err
		}
	}
	return nil
}

// buildCompletionAdapters rejects opaque field representations and keeps
// only the typed binding needed to call the service for a selected argument.
func (g *adapterGenerator) buildCompletionAdapters() ([]*completionAdapter, error) {
	adapters := make([]*completionAdapter, 0, len(g.mcp.PromptCompletions)+len(g.mcp.ResourceCompletions))
	for _, completion := range g.mcp.PromptCompletions {
		if err := validateExecutionViews(completion.Method, func(result *expr.AttributeExpr) error {
			method := *completion.Method
			method.Result = result
			selected := *completion
			selected.Method = &method
			return selected.Validate()
		}); err != nil {
			return nil, err
		}
		if err := completion.Validate(); err != nil {
			return nil, err
		}
		if err := checkContentGoType(completion.Method.Payload); err != nil {
			return nil, err
		}
		if err := checkContentGoType(completion.Method.Result); err != nil {
			return nil, err
		}
		adapters = append(adapters, &completionAdapter{ReferenceType: "ref/prompt", Reference: completion.Prompt, Argument: completion.Argument, method: completion.Method})
	}
	for _, completion := range g.mcp.ResourceCompletions {
		if err := validateExecutionViews(completion.Method, func(result *expr.AttributeExpr) error {
			method := *completion.Method
			method.Result = result
			selected := *completion
			selected.Method = &method
			return selected.Validate()
		}); err != nil {
			return nil, err
		}
		if err := completion.Validate(); err != nil {
			return nil, err
		}
		if err := checkContentGoType(completion.Method.Payload); err != nil {
			return nil, err
		}
		if err := checkContentGoType(completion.Method.Result); err != nil {
			return nil, err
		}
		adapters = append(adapters, &completionAdapter{ReferenceType: "ref/resource", Reference: completion.URI, Argument: completion.Argument, method: completion.Method})
	}
	return adapters, nil
}

// buildCompletionReferences derives accepted names from authored declarations.
// Runtime selection does not parse templates or rebuild prompt schemas.
func (g *adapterGenerator) buildCompletionReferences(templates []*resourceTemplateAdapter) ([]*completionReferenceAdapter, error) {
	references := make([]*completionReferenceAdapter, 0, len(g.mcp.MethodPrompts)+len(templates))
	for _, prompt := range g.mcp.MethodPrompts {
		reference := &completionReferenceAdapter{Type: "ref/prompt", Name: prompt.Name}
		arguments, err := mcpinput.Arguments(prompt.Method.Payload)
		if err != nil {
			return nil, err
		}
		if hasMCPValue(arguments) {
			for _, field := range *expr.AsObject(arguments.Type) {
				reference.Arguments = append(reference.Arguments, field.Name)
			}
		}
		references = append(references, reference)
	}
	for _, template := range templates {
		references = append(references, &completionReferenceAdapter{Type: "ref/resource", Name: template.URI, Arguments: template.Variables})
	}
	return references, nil
}
