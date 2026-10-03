// Package codegen binds prompt-completion methods to generated payload
// constructors and result conversions. Names and field types are decided during
// generation; runtime code only selects the requested prompt and argument.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// promptCompletionAdapter retains the one method selected by a DSL binding.
	promptCompletionAdapter struct {
		// Prompt is the declared prompt name selected by the client.
		Prompt string
		// Argument is the declared argument being completed.
		Argument string
		// ServiceMethodName is Goa's final service method name.
		ServiceMethodName string
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

		authored  *mcpexpr.PromptCompletionExpr
		transform *codegen.TransformPlan
	}
)

// planCompletionConversions registers complete result imports and transform
// helper names before Goa freezes names. The target is the real protocol type.
func planCompletionConversions(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	if len(data.PromptCompletions) == 0 {
		return nil
	}
	target := expr.AsObject(prepared.mcpService.Method("completion/complete").Result.Type).Attribute("completion")
	pkg := generation.Package(data.mcpImportPath)
	imports := codegen.NewGeneratedImportPlan(pkg)
	for index, completion := range data.PromptCompletions {
		layout, err := services.MethodTypeLayout(completion.authored.Method, completion.authored.Method.Result)
		if err != nil {
			return err
		}
		if err := imports.AddCompleteType(layout); err != nil {
			return err
		}
		transform, err := codegen.NewTransformPlan(completion.authored.Method.Result, target, "completion", nil)
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
		completion.transform = transform
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
	sourceScope := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
	targetScope := services.ServiceAttributor(planned.prepared.mcpService.Name, data.mcpImportPath)
	source := &codegen.AttributeContext{Scope: sourceScope, UseDefault: true}
	target := &codegen.AttributeContext{Scope: targetScope, UseDefault: true}
	for _, completion := range data.PromptCompletions {
		method := completion.authored.Method
		completion.ServiceMethodName = services.Get(planned.prepared.userService.Name).Method(method.Name).VarName
		values := planned.methodCodecs[method.Name]
		completion.Codec = methodCodecData(values)
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
		completion.Conversion, completion.Helpers, err = completion.transform.Render("result", "out", true)
		if err != nil {
			return err
		}
	}
	return nil
}

// buildPromptCompletionAdapters rejects opaque field representations and keeps
// only the typed binding needed to call the service for a selected argument.
func (g *adapterGenerator) buildPromptCompletionAdapters() ([]*promptCompletionAdapter, error) {
	adapters := make([]*promptCompletionAdapter, 0, len(g.mcp.PromptCompletions))
	for _, completion := range g.mcp.PromptCompletions {
		if err := completion.Validate(); err != nil {
			return nil, err
		}
		if err := checkPromptGoType(completion.Method.Payload); err != nil {
			return nil, err
		}
		if err := checkPromptGoType(completion.Method.Result); err != nil {
			return nil, err
		}
		adapters = append(adapters, &promptCompletionAdapter{Prompt: completion.Prompt, Argument: completion.Argument, authored: completion})
	}
	return adapters, nil
}
