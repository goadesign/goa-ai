// Package codegen connects configured MCP endpoints to shared typed input
// conversion. Goa's selected view remains the source of pending result fields.
package codegen

import (
	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	inputexchange "goa.design/goa-ai/codegen/internal/inputexchange"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type inputExchangeAdapter = inputexchange.Plan

// planInputExchangeCodecs preserves the endpoint's pending view and plans the
// same answer decoders used by generated native callers.
func planInputExchangeCodecs(services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData, codecs *jsoncodec.Plan, call *endpointMethodAdapter) error {
	mapping, err := mcpinput.InputExchange(call.method)
	if err != nil || mapping == nil {
		return err
	}
	var pending *expr.AttributeExpr
	for _, branch := range expr.AsUnion(call.resultAttribute.Find(mapping.OutcomeName).Type).Values {
		if branch.Name == "input_required" {
			pending = expr.DupAtt(branch.Attribute)
			break
		}
	}
	if err := selectResultFields(pending, mapping.Pending, make(map[resultTypePair]expr.DataType)); err != nil {
		return err
	}
	input, err := inputexchange.New(services, prepared.root.API, call.method, pending, codecs)
	if err != nil {
		return err
	}
	call.InputExchange = input
	data.NeedsServerCodec = true
	return nil
}

// bindInputExchange supplies the configured endpoint's final result expression.
// Only completed values enter the ordinary result conversion.
func bindInputExchange(data *AdapterData, call *endpointMethodAdapter) error {
	input := call.InputExchange
	if input == nil {
		return nil
	}
	mapping, err := mcpinput.InputExchange(call.method)
	if err != nil {
		return err
	}
	outcome := call.ResultValue + "." + codegen.GoifyAtt(call.resultAttribute.Find(mapping.OutcomeName), mapping.OutcomeName, true)
	if err := input.Bind(data.mcpImportPath, data.mcpPackage.ImportName, data.CodecPackage, outcome); err != nil {
		return err
	}
	call.ResultValue = "completedResult"
	return nil
}
