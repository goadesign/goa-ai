// Package codegen keeps authored URL values separate from MCP parameters. Each
// selected service method receives URL values decoded with Goa's HTTP templates
// and converted into its original payload before full validation and dispatch.
package codegen

import (
	"bytes"
	"fmt"
	"slices"
	"text/template"

	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
	httpcodegen "goa.design/goa/v3/http/codegen"
)

type (
	// methodRouteInput retains the selected method's typed URL decoding.
	methodRouteInput struct {
		Name       string
		ValueName  string
		ValueRef   string
		Conversion string
		Decode     string
		Sources    map[string]string
		attribute  *expr.AttributeExpr
		layout     *codegen.GoTypePlan
		target     *codegen.GoTypePlan
		transform  *codegen.TransformPlan
		sourceName string
	}
	// callerRouteInput binds a URL value when the application constructs a caller.
	callerRouteInput struct {
		Name     string
		Selector string
	}
	// routeParser supplies the existing HTTP parsing template's selected facts.
	routeParser struct {
		Name              string
		VarName           string
		Type              expr.DataType
		TypeRef           string
		TypeName          string
		Pointer           bool
		IsTextUnmarshaler bool
	}
)

// prepareRouteInputs keeps complete authored paths, including API and parent
// prefixes. Goa's mapped attributes bind private protocol fields to the original
// URL names, including names that also occur in MCP parameter bodies.
func prepareRouteInputs(service *expr.HTTPServiceExpr) (*expr.MappedAttributeExpr, []string) {
	route := *service.JSONRPCRoute
	route.Endpoint = &expr.HTTPEndpointExpr{Service: service}
	inputs := expr.NewEmptyMappedAttributeExpr()
	for index, name := range route.Params() {
		field := fmt.Sprintf("httpPath%d", index)
		expr.AsObject(inputs.Type).Set(field, &expr.AttributeExpr{
			Type:        expr.String,
			Description: "URL value for " + name + "; excluded from MCP parameters.",
		})
		inputs.Map(name, field)
	}
	return inputs, route.FullPaths()
}

// planRouteInputs retains protocol selectors and each method's URL value layout
// before names freeze. Conversion helpers and custom type imports belong to the
// same generated package as the endpoint adapter.
func planRouteInputs(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	prepared.protocolLayouts = make(map[string]*codegen.GoTypePlan)
	for _, method := range prepared.mcpService.Methods {
		layout, err := services.MethodTypeLayout(method, method.Payload)
		if err != nil {
			return err
		}
		prepared.protocolLayouts[method.Name] = layout
	}
	pkg := generation.Package(data.mcpImportPath)
	imports := codegen.NewGeneratedImportPlan(pkg)
	for index, endpoint := range data.EndpointMethods {
		bindings := mcpinput.PathParams(prepared.transport, endpoint.method)
		// Only bound URL values enter the domain payload. Goa validates their
		// actual types; other route values continue to address the MCP server.
		bound := expr.DupMappedAtt(bindings)
		for _, input := range *expr.AsObject(bindings.Type) {
			if endpoint.method.Payload.Find(input.Name) == nil {
				bound.Delete(input.Name)
			}
		}
		route := *prepared.transport.JSONRPCRoute
		validation := &expr.HTTPEndpointExpr{
			MethodExpr: endpoint.method, Service: prepared.transport,
			Params: bound, Routes: []*expr.RouteExpr{&route},
		}
		route.Endpoint = validation
		if errors := validation.ValidateParams(); errors != nil && len(errors.Errors) > 0 {
			return errors
		}
		for _, input := range *expr.AsObject(bound.Type) {
			attribute := endpoint.method.Payload.Find(input.Name)
			wire := httpcodegen.WireAttribute(attribute)
			layout, err := codegen.PlanGoType(wire, codegen.GoTypePlanOptions{
				Owner: data.mcpImportPath, Policy: codegen.GoLayoutPolicy{UseDefault: true, SumType: true},
			})
			if err != nil {
				return err
			}
			if err := imports.AddCompleteType(layout); err != nil {
				return err
			}
			targets := endpoint.payloadLayout.PlansForOccurrence(attribute)
			if len(targets) != 1 || targets[0].FieldName(true) == "" {
				return fmt.Errorf("MCP method %q URL field %q must resolve to one payload field", endpoint.method.Name, input.Name)
			}
			transform, err := codegen.NewTransformPlan(wire, attribute, "route", nil)
			if err != nil {
				return err
			}
			for helperIndex, helper := range transform.Helpers() {
				declaration := codegen.NewExactName(codegen.NameFunction, fmt.Sprintf("convertRoute%dField%dHelper%d", index, len(endpoint.Paths), helperIndex))
				if err := pkg.DeclareName(declaration); err != nil {
					return err
				}
				if err := transform.BindHelperDeclaration(helper.ID, declaration); err != nil {
					return err
				}
			}
			endpoint.Paths = append(endpoint.Paths, &methodRouteInput{
				Name: input.Name, ValueName: fmt.Sprintf("routeValue%d", len(endpoint.Paths)),
				Sources: make(map[string]string), attribute: wire,
				layout: layout, target: targets[0], transform: transform,
				sourceName: prepared.paths.KeyName(bindings.ElemName(input.Name)),
			})
		}
	}
	for _, importPath := range imports.Paths() {
		if importPath != data.mcpImportPath {
			data.serverImportPaths = append(data.serverImportPaths, importPath)
		}
	}
	return nil
}

// bindRouteInputs renders decoding and conversion from retained layouts after
// names freeze. Native caller fields and protocol payload selectors use the same
// recorded service layouts as the generated HTTP client and server.
func bindRouteInputs(services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	data.PayloadRefs = make(map[string]string, len(prepared.protocolLayouts))
	for operation, layout := range prepared.protocolLayouts {
		data.PayloadRefs[operation] = layout.Link(data.mcpImportPath, data.mcpPackage.ImportName).Ref()
	}
	if caller := data.ClientCaller; caller != nil {
		layout := prepared.protocolLayouts["tools/call"]
		caller.PayloadRef = layout.Link(caller.clientPackage.ImportPath(), caller.clientPackage.ImportName).RefWithPointer(false)
		for _, input := range *expr.AsObject(prepared.paths.Type) {
			field, err := protocolRouteSelector(prepared, layout, "tools/call", input.Name)
			if err != nil {
				return err
			}
			caller.Paths = append(caller.Paths, &callerRouteInput{Name: input.Name, Selector: field})
		}
	}
	for _, endpoint := range data.EndpointMethods {
		for _, input := range endpoint.Paths {
			linked := input.layout.Link(data.mcpImportPath, data.mcpPackage.ImportName)
			input.ValueRef = linked.RefWithPointer(false)
			decode, err := renderRouteParser(input)
			if err != nil {
				return err
			}
			input.Decode = decode
			for operation, layout := range prepared.protocolLayouts {
				selector, err := protocolRouteSelector(prepared, layout, operation, input.sourceName)
				if err != nil {
					return err
				}
				input.Sources[operation] = selector
			}
			source := codegen.NewAttributeContext(false, false, true, "", codegen.NewNameScope())
			source, err = source.WithGoTypeLayout(linked)
			if err != nil {
				return err
			}
			target := &codegen.AttributeContext{Scope: services.Services().ServiceAttributor(prepared.userService.Name, data.mcpImportPath), UseDefault: true}
			targetLayout := input.target.Link(data.mcpImportPath, data.mcpPackage.ImportName)
			target, err = target.WithGoTypeLayout(targetLayout)
			if err != nil {
				return err
			}
			if err := input.transform.BindContexts(source, target); err != nil {
				return err
			}
			var helpers []*codegen.TransformFunctionData
			selector := "payload." + targetLayout.Field(true)
			if input.target.IsPointer() {
				// An optional service field stores a pointer. Convert its URL
				// value first, then assign that value's address to the field.
				value := input.ValueName + "Typed"
				input.Conversion, helpers, err = input.transform.Render(input.ValueName, value, true)
				input.Conversion += "\n" + selector + " = &" + value
			} else {
				input.Conversion, helpers, err = input.transform.Render(input.ValueName, selector, false)
			}
			if err != nil {
				return err
			}
			endpoint.RouteHelpers = append(endpoint.RouteHelpers, helpers...)
		}
	}
	return nil
}

// protocolRouteSelector finds the one planned protocol field that carries a URL
// value. Missing or duplicated fields are generator errors rather than runtime
// guesses about the request's payload shape.
func protocolRouteSelector(prepared *preparedMCPService, layout *codegen.GoTypePlan, operation, name string) (string, error) {
	attribute := prepared.mcpService.Method(operation).Payload.Find(name)
	fields := layout.PlansForOccurrence(attribute)
	if len(fields) != 1 || fields[0].FieldName(true) == "" {
		return "", fmt.Errorf("MCP operation %q URL input %q must resolve to one payload field", operation, name)
	}
	return fields[0].FieldName(true), nil
}

// renderRouteParser writes the HTTP conversion selected for one URL field.
// Plain strings remain exact input text; custom string types use UnmarshalText.
func renderRouteParser(input *methodRouteInput) (string, error) {
	custom, _ := codegen.GetMetaType(input.attribute)
	if (input.attribute.Type == expr.String && custom == "") || input.attribute.Type == expr.Any {
		return input.ValueName + " = " + input.ValueName + "Raw", nil
	}
	parser := routeParser{
		Name: input.Name, VarName: input.ValueName, Type: input.attribute.Type,
		TypeRef: input.ValueRef, TypeName: input.ValueRef,
		IsTextUnmarshaler: input.attribute.Type == expr.String && custom != "",
	}
	tmpl, err := template.New("path_conversion").Funcs(template.FuncMap{
		"goTypeRef": func(_ expr.DataType) string { return input.ValueRef },
	}).Parse(httpcodegen.ReadTemplate("partial/path_conversion", "query_type_conversion", "slice_item_conversion"))
	if err != nil {
		return "", err
	}
	var source bytes.Buffer
	if err := tmpl.Execute(&source, parser); err != nil {
		return "", err
	}
	return source.String(), nil
}

// routeInputImports reserves only standard parsers actually emitted for URL
// fields. Custom field types contribute their imports through retained layouts.
func routeInputImports(data *AdapterData) []*codegen.ImportSpec {
	var numeric, collection bool
	for _, endpoint := range data.EndpointMethods {
		for _, input := range endpoint.Paths {
			typeName := input.attribute.Type.Name()
			if array := expr.AsArray(input.attribute.Type); array != nil {
				collection = true
				typeName = array.ElemType.Type.Name()
			}
			numeric = numeric || slices.Contains([]string{"int", "int32", "int64", "uint", "uint32", "uint64", "float32", "float64", "boolean"}, typeName)
		}
	}
	var imports []*codegen.ImportSpec
	if numeric {
		imports = append(imports, codegen.SimpleImport("strconv"))
	}
	if collection {
		imports = append(imports, codegen.SimpleImport("strings"))
	}
	return imports
}
