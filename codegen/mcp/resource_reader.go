// Package codegen plans the one typed service method that owns exact URI reads. It uses Goa's final field names and the same text/blob conversion used
// by embedded prompt resources; the service receives no inferred variables.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// resourceReaderAdapter contains the private constructor and result copy.
	resourceReaderAdapter struct {
		// Endpoint calls the configured Goa endpoint for this method.
		Endpoint *endpointMethodAdapter
		// PayloadTransportRef is the private decoded-input type.
		PayloadTransportRef string
		// PayloadConstructor validates input and constructs the service payload.
		PayloadConstructor string
		// URI is the private input field retaining the client's exact address.
		URI *jsoncodec.TransportField
		// ContentsField is the service result's ordered content array.
		ContentsField string
		// ContentField is the selected text/blob union on one result item.
		ContentField string
		// ContentConversion copies that union to the flat MCP resource shape.
		ContentConversion string
		// Codec names the generated result validator.
		Codec *MethodCodecData

		method          *expr.MethodExpr
		conversion      *contentConversion
		resultAttribute *expr.AttributeExpr
	}
)

// buildResourceReaderAdapter rejects service fields that conversion would lose.
func (g *adapterGenerator) buildResourceReaderAdapter() (*resourceReaderAdapter, error) {
	if g.mcp.ResourceReader == nil {
		return nil, nil
	}
	method := g.mcp.ResourceReader
	if err := validateExecutionViews(method, func(result *expr.AttributeExpr) error {
		selected := *method
		selected.Result = result
		return mcpexpr.ValidateResourceReader(&selected)
	}); err != nil {
		return nil, err
	}
	if err := mcpexpr.ValidateResourceReader(method); err != nil {
		return nil, err
	}
	if err := checkContentGoType(method.Payload); err != nil {
		return nil, err
	}
	if err := checkContentGoType(method.Result); err != nil {
		return nil, err
	}
	completed, err := mcpinput.CompleteResult(method)
	if err != nil {
		return nil, err
	}
	contents := expr.AsArray(expr.AsObject(completed.Type).Attribute("contents").Type)
	content := expr.AsObject(contents.ElemType.Type).Attribute("content")
	builder := newMCPExprBuilder(g.originalService, g.mcp)
	target := builder.getOrCreateType("ResourceContent", builder.buildResourceContentType)
	conversion, err := buildContentConversion(content, &expr.AttributeExpr{Type: target}, false)
	if err != nil {
		return nil, fmt.Errorf("resource reader %q: %w", method.Name, err)
	}
	return &resourceReaderAdapter{method: method, conversion: conversion}, nil
}

// planResourceReader records final type dependencies before Goa freezes names.
func planResourceReader(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	reader := data.ResourceReader
	if reader == nil {
		return nil
	}
	result, layout, err := planMCPResult(services, reader.method)
	if err != nil {
		return err
	}
	method := *reader.method
	method.Result = result
	method.Payload, err = mcpinput.Arguments(reader.method)
	if err != nil {
		return err
	}
	method.Meta = make(expr.MetaExpr)
	for key, value := range reader.method.Meta {
		if key != mcpinput.ExchangeMetaKey {
			method.Meta[key] = value
		}
	}
	if err := mcpexpr.ValidateResourceReader(&method); err != nil {
		return fmt.Errorf("resource reader selected result view: %w", err)
	}
	reader.resultAttribute = result
	pkg := generation.Package(data.mcpImportPath)
	imports := codegen.NewGeneratedImportPlan(pkg)
	if err := imports.AddCompleteType(layout); err != nil {
		return err
	}
	for _, importPath := range imports.Paths() {
		if importPath != data.mcpImportPath {
			data.serverImportPaths = append(data.serverImportPaths, importPath)
		}
	}
	target := expr.AsArray(expr.AsObject(protocolCompletedResult(prepared.mcpService.Method("resources/read").Result).Type).Attribute("contents").Type).ElemType
	contents := expr.AsObject(result.Type).Attribute("contents")
	if contents == nil {
		return fmt.Errorf("resource reader selected view omits contents")
	}
	content := expr.AsObject(expr.AsArray(contents.Type).ElemType.Type).Attribute("content")
	reader.conversion, err = buildContentConversion(content, target, false)
	if err != nil {
		return err
	}
	if err := planContentConversion(generation, pkg, reader.conversion, target, layout, "convertResourceContent"); err != nil {
		return err
	}
	contentConversionNeeds(data, reader.conversion)
	return nil
}

// bindResourceReader uses the saved field layout to construct a typed request
// and copy validated content without JSON serialization or URI normalization.
func bindResourceReader(services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	reader := data.ResourceReader
	if reader == nil {
		return nil
	}
	values := planned.methodCodecs[reader.method.Name]
	reader.Codec = methodCodecData(values, data.CodecPackage)
	var err error
	reader.PayloadTransportRef, err = values.payload.TransportTypeName(data.mcpImportPath, data.mcpPackage.ImportName)
	if err != nil {
		return err
	}
	reader.PayloadConstructor = values.payload.TransportConstructorDeclaration().Name()
	payload := expr.AsObject(reader.method.Payload.Type)
	reader.URI, err = values.payload.TransportField(payload.Attribute("uri"), "uri", data.mcpImportPath, data.mcpPackage.ImportName)
	if err != nil {
		return err
	}
	source := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
	if reader.Endpoint.ProjectedResult || reader.Endpoint.ExecutionView {
		source = services.ViewAttributor(planned.prepared.userService.Name, data.mcpImportPath)
	}
	target := services.ServiceAttributor(planned.prepared.mcpService.Name, data.mcpImportPath)
	contents := expr.AsObject(reader.resultAttribute.Type).Attribute("contents")
	reader.ContentsField = source.Field(contents, "contents", true)
	content := expr.AsObject(expr.AsArray(contents.Type).ElemType.Type).Attribute("content")
	reader.ContentField = source.Field(content, "content", true)
	if err := bindContentConversion(data, reader.conversion, source, target); err != nil {
		return err
	}
	reader.ContentConversion = reader.conversion.declaration.Name()
	return nil
}
