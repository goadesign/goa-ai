// Package codegen emits typed tool content and structured results from one Goa
// endpoint result. Content is validated and converted separately so audience
// annotations cannot be bypassed through the tool's structured JSON fields.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/mcpcontract"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// toolContentAdapter supplies a generated result converter for one tool.
	toolContentAdapter struct {
		Name          string
		SourceRef     string
		Validate      string
		ExecutionView bool
		Cases         []*toolContentCase
		// OutcomeValue selects the completed branch inside a service-selected view.
		OutcomeValue string

		tool        *mcpexpr.ToolExpr
		declaration *codegen.NameDeclaration
	}
	// toolContentCase records the fields returned by one declared view.
	toolContentCase struct {
		Name         string
		Field        string
		ElementField string
		Convert      string
		Encode       string
		Value        string

		result     *expr.AttributeExpr
		layout     *codegen.GoTypePlan
		conversion *contentConversion
		structured *jsoncodec.Value
	}
)

// buildToolContentAdapter checks every selected view before planning converters.
// A view may omit content; an included content field must retain its full union.
func (g *adapterGenerator) buildToolContentAdapter(tool *mcpexpr.ToolExpr) (*toolContentAdapter, error) {
	if err := tool.Validate(); err != nil {
		return nil, err
	}
	adapter := &toolContentAdapter{tool: tool}
	owner := tool.Method
	if task, err := mcpinput.TaskExchange(tool.Method); err != nil {
		return nil, err
	} else if task != nil {
		owner = task.Read
	}
	if result, viewed := owner.Result.Type.(*expr.ResultTypeExpr); viewed {
		if view, fixed := mcpcontract.FixedView(owner.Result); fixed {
			selected, err := mcpcontract.ResultView(tool.Method, view)
			if err != nil {
				return nil, err
			}
			adapter.Cases = append(adapter.Cases, &toolContentCase{Name: view, result: selected})
		} else {
			adapter.ExecutionView = true
			for _, view := range result.Views {
				selected, err := mcpcontract.ResultView(tool.Method, view.Name)
				if err != nil {
					return nil, err
				}
				adapter.Cases = append(adapter.Cases, &toolContentCase{Name: view.Name, result: selected})
			}
		}
	} else {
		completed, err := mcpcontract.Result(tool.Method)
		if err != nil {
			return nil, err
		}
		adapter.Cases = append(adapter.Cases, &toolContentCase{result: completed})
	}
	builder := newMCPExprBuilder(g.originalService, g.mcp)
	target := &expr.AttributeExpr{Type: builder.getOrCreateType("ContentItem", builder.buildContentItemType)}
	for _, selected := range adapter.Cases {
		attribute := expr.AsObject(selected.result.Type).Attribute(tool.ContentField)
		if attribute == nil {
			continue
		}
		array := expr.AsArray(attribute.Type)
		if err := checkContentGoType(attribute); err != nil {
			return nil, fmt.Errorf("tool %q content: %w", tool.Name, err)
		}
		if array == nil || !array.NonNullableElems {
			return nil, fmt.Errorf("tool %q content must use ArrayOfRequired", tool.Name)
		}
		if err := checkContentFields(array.ElemType, []string{"content"}); err != nil {
			return nil, fmt.Errorf("tool %q content item: %w", tool.Name, err)
		}
		conversion, err := buildContentConversion(expr.AsObject(array.ElemType.Type).Attribute("content"), target, true)
		if err != nil {
			return nil, fmt.Errorf("tool %q view %q content: %w", tool.Name, selected.Name, err)
		}
		selected.conversion = conversion
	}
	return adapter, nil
}

// planToolContent reserves typed converters and codecs for the fields remaining
// after the content array is excluded. Full endpoint results retain their own
// validation; these codecs encode only the structured tool contract.
func planToolContent(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData, codecs *jsoncodec.Plan, methods map[string]*plannedMethodCodec) error {
	pkg := generation.Package(data.mcpImportPath)
	imports := codegen.NewGeneratedImportPlan(pkg)
	var target *expr.AttributeExpr
	for _, method := range prepared.mcpService.Methods {
		if method.Name != "tools/call" {
			continue
		}
		target = expr.AsArray(expr.AsObject(protocolCompletedResult(method.Result).Type).Attribute("content").Type).ElemType
	}
	for index, tool := range data.Tools {
		content := tool.Content
		if content == nil {
			continue
		}
		content.declaration = codegen.NewExactName(codegen.NameFunction, fmt.Sprintf("convertTool%dResult", index))
		if err := pkg.DeclareName(content.declaration); err != nil {
			return err
		}
		values := methods[tool.userMethodName]
		if err := values.planResultValidation(); err != nil {
			return err
		}
		owner := content.tool.Method
		if tool.Task != nil {
			owner = tool.Task.binding.Read
		}
		for viewIndex, selected := range content.Cases {
			var err error
			if _, viewed := owner.Result.Type.(*expr.ResultTypeExpr); viewed {
				selected.result, selected.layout, err = planMCPResultView(services, content.tool.Method, selected.Name)
			} else {
				selected.result, selected.layout, err = planMCPResult(services, content.tool.Method)
			}
			if err != nil {
				return err
			}
			if err := imports.AddCompleteType(selected.layout); err != nil {
				return err
			}
			if attribute := expr.AsObject(selected.result.Type).Attribute(content.tool.ContentField); attribute != nil {
				selected.conversion, err = buildContentConversion(expr.AsObject(expr.AsArray(attribute.Type).ElemType.Type).Attribute("content"), target, true)
				if err != nil {
					return err
				}
				if err := planContentConversion(generation, pkg, selected.conversion, target, selected.layout, fmt.Sprintf("convertTool%dView%dContent", index, viewIndex), codecs); err != nil {
					return err
				}
				contentConversionNeeds(data, selected.conversion)
			}
			structured, err := mcpcontract.WithoutField(selected.result, content.tool.ContentField)
			if err != nil {
				return err
			}
			if len(*expr.AsObject(structured.Type)) == 0 && !content.ExecutionView {
				continue
			}
			layout, err := services.MethodTypeLayout(owner, structured)
			if err != nil {
				return err
			}
			selected.structured, err = codecs.Add(fmt.Sprintf("%s:%s:tool:%s:view:%d", prepared.userService.Name, tool.userMethodName, tool.Name, viewIndex), fmt.Sprintf("Tool%dView%dStructuredResult", index, viewIndex), structured, layout, jsoncodec.EncodeOnly)
			if err != nil {
				return err
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

// bindToolContent joins result fields to Goa's final names and renders the shared
// content conversions. Each declared view selects only its own returned fields.
func bindToolContent(services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	target := services.ServiceAttributor(planned.prepared.mcpService.Name, data.mcpImportPath)
	for _, tool := range data.Tools {
		content := tool.Content
		if content == nil {
			continue
		}
		endpoint := tool.Endpoint
		if tool.Task != nil {
			endpoint = tool.Task.Read
		}
		source := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
		if endpoint.ProjectedResult || endpoint.ExecutionView {
			source = services.ViewAttributor(planned.prepared.userService.Name, data.mcpImportPath)
		}
		content.Name = content.declaration.Name()
		content.SourceRef = endpoint.ResultRef
		content.Validate = endpoint.Codec.ResultValidate
		if input := tool.Endpoint.InputExchange; input != nil {
			if tool.Endpoint.ExecutionView {
				content.OutcomeValue = input.OutcomeValue
			} else {
				content.SourceRef = content.Cases[0].layout.Link(data.mcpImportPath, data.mcpPackage.ImportName).Ref()
			}
		}
		if task := tool.Task; task != nil {
			// Task reads validate the full observation before this private converter.
			content.Validate = ""
			content.ExecutionView = task.Read.ExecutionView
			if task.Read.ExecutionView {
				content.SourceRef = task.Read.ResultRef
				content.OutcomeValue = task.Read.ResultValue + "." + task.Observed.OutcomeField
			} else {
				content.SourceRef = content.Cases[0].layout.Link(data.mcpImportPath, data.mcpPackage.ImportName).Ref()
				content.OutcomeValue = ""
			}
		}
		for _, selected := range content.Cases {
			selected.Value = endpoint.ResultValue
			if !content.ExecutionView {
				selected.Value = "result"
			} else if tool.Task != nil {
				selected.Value = "completedResult"
			}
			if selected.conversion != nil {
				attribute := expr.AsObject(selected.result.Type).Attribute(content.tool.ContentField)
				selected.Field = source.Field(attribute, content.tool.ContentField, true)
				element := expr.AsObject(expr.AsArray(attribute.Type).ElemType.Type).Attribute("content")
				selected.ElementField = source.Field(element, "content", true)
				if err := bindContentConversion(data, selected.conversion, source, target); err != nil {
					return err
				}
				selected.Convert = selected.conversion.declaration.Name()
			}
			if selected.structured != nil {
				selected.Encode = data.CodecPackage + "." + selected.structured.EncodeDeclaration().Name()
			}
		}
	}
	return nil
}
