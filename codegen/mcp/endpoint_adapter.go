// Package codegen connects validated MCP arguments to the application's configured Goa
// endpoints. Each authored method has one generated call that preserves endpoint
// authentication and middleware and checks the returned Go type before conversion.
package codegen

import (
	"fmt"

	"goa.design/goa-ai/codegen/internal/mcpcontract"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// endpointMethodAdapter retains the service types used by one endpoint call.
	endpointMethodAdapter struct {
		// CallName names the private typed endpoint invocation.
		CallName string
		// MethodName selects the original configured Goa endpoint.
		MethodName string
		// DesignMethodName is the authored method identity observed by middleware.
		DesignMethodName string
		// PayloadRef is the original service input type; empty means no input.
		PayloadRef string
		// ResultRef is the original service output type; empty means no output.
		ResultRef string
		// EndpointResultRef is the Go value returned by the endpoint.
		EndpointResultRef string
		// ResultConstructor copies an execution-selected view to its declared result type.
		ResultConstructor string
		// ProjectedResult selects the view fields already returned by the endpoint.
		ProjectedResult bool

		method        *expr.MethodExpr
		payloadLayout *codegen.GoTypePlan
		resultLayout  *codegen.GoTypePlan
	}
)

// planEndpointAdapters records original payload and result imports before Goa
// chooses package names. Protocol requests never call an unwrapped service.
func planEndpointAdapters(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	pkg := generation.Package(data.mcpImportPath)
	imports := codegen.NewGeneratedImportPlan(pkg)
	for index, method := range mappedMCPMethods(prepared) {
		_, views, err := services.MethodPackageImports(method)
		if err != nil {
			return err
		}
		if views != nil {
			if err := imports.AddGenerated(views); err != nil {
				return err
			}
		}
		call := &endpointMethodAdapter{method: method, CallName: fmt.Sprintf("invokeMCPMethod%d", index), DesignMethodName: method.Name}
		for _, side := range []struct {
			attribute *expr.AttributeExpr
			layout    **codegen.GoTypePlan
		}{
			{method.Payload, &call.payloadLayout},
			{method.Result, &call.resultLayout},
		} {
			if !hasMCPValue(side.attribute) {
				continue
			}
			var layout *codegen.GoTypePlan
			var err error
			if side.attribute == method.Result {
				_, layout, err = planMCPResult(services, method)
			} else {
				layout, err = services.MethodTypeLayout(method, side.attribute)
			}
			if err != nil {
				return err
			}
			if err := imports.AddCompleteType(layout); err != nil {
				return err
			}
			*side.layout = layout
		}
		data.NeedsEndpointResultCheck = data.NeedsEndpointResultCheck || call.resultLayout != nil
		data.EndpointMethods = append(data.EndpointMethods, call)
		for _, tool := range data.Tools {
			if tool.userMethodName == method.Name {
				tool.Endpoint = call
			}
		}
		for _, resource := range data.Resources {
			if resource.userMethodName == method.Name {
				resource.Endpoint = call
			}
		}
		for _, prompt := range data.MethodPrompts {
			if prompt.prompt.Method == method {
				prompt.Endpoint = call
			}
		}
		if reader := data.ResourceReader; reader != nil && reader.method == method {
			reader.Endpoint = call
		}
		for _, completion := range data.Completions {
			if completion.method == method {
				completion.Endpoint = call
			}
		}
	}
	for _, importPath := range imports.Paths() {
		if importPath != data.mcpImportPath {
			data.serverImportPaths = append(data.serverImportPaths, importPath)
		}
	}
	return nil
}

// bindEndpointAdapters uses Goa's saved declarations for selectors and type
// references. A fixed view supplies its selected fields directly to its codec.
func bindEndpointAdapters(service *goaservice.Data, data *AdapterData) error {
	data.EndpointsName = service.EndpointsDeclaration.Name()
	for _, call := range data.EndpointMethods {
		method := service.Method(call.method.Name)
		call.MethodName = method.VarName
		if call.payloadLayout != nil {
			call.PayloadRef = call.payloadLayout.Link(data.mcpImportPath, data.mcpPackage.ImportName).Ref()
		}
		if call.resultLayout == nil {
			continue
		}
		call.ResultRef = call.resultLayout.Link(data.mcpImportPath, data.mcpPackage.ImportName).Ref()
		call.EndpointResultRef = call.ResultRef
		if view := method.ViewedResult; view != nil {
			layout, err := codegen.PlanGoType(&expr.AttributeExpr{Type: view.Type}, codegen.GoTypePlanOptions{
				Owner: view.Declaration.PackagePath(),
				Bind: func(request codegen.GoTypeBindingRequest) (codegen.GoTypeBinding, error) {
					if request.Kind != codegen.GoNamed || request.Attribute.Type != view.Type {
						return codegen.GoTypeBinding{}, fmt.Errorf("viewed endpoint result has an unexpected type")
					}
					return codegen.GoTypeBinding{Owner: view.Declaration.PackagePath(), Type: view.Declaration}, nil
				},
			})
			if err != nil {
				return fmt.Errorf("bind MCP endpoint view for %q: %w", call.method.Name, err)
			}
			call.EndpointResultRef = layout.Link(data.mcpImportPath, data.mcpPackage.ImportName).Ref()
			if _, fixed := mcpcontract.FixedView(call.method); fixed {
				call.ProjectedResult = true
			} else {
				call.ResultConstructor = data.Package + "." + view.ResultInit.Declaration.Name()
			}
		}
	}
	return nil
}
