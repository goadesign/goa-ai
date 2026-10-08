// Package codegen connects changing catalog pages to configured Goa endpoints.
// Application methods return declared operation names or resource descriptors
// and opaque cursors. Generation retains native field layouts, supplies authored
// operation metadata and copies validated resource descriptors without JSON.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// catalogAdapter retains one typed page owner and its input/output layouts.
	catalogAdapter struct {
		// Endpoint invokes the configured original catalog method.
		Endpoint *endpointMethodAdapter
		// PayloadTransportRef names the private cursor input record.
		PayloadTransportRef string
		// PayloadConstructor validates the cursor and constructs the native payload.
		PayloadConstructor string
		// Cursor retains Goa's exact input field, alias and pointer representation.
		Cursor *jsoncodec.TransportField
		// Operation names the fixed protocol list method for native HTTP inputs.
		Operation string
		// EntriesField selects the validated native catalog entries.
		EntriesField string
		// NamePointer records whether each returned name is a pointer in this view.
		NamePointer bool
		// NextCursor copies the native optional cursor using Goa's conversion plan.
		NextCursor string
		// EntriesConversion copies typed runtime descriptors to the protocol array.
		EntriesConversion string
		// Helpers contains Goa conversions for nested descriptor types.
		Helpers []*codegen.TransformFunctionData
		// CheckMeta, CheckSize and CheckPriority select declared metadata checks.
		CheckMeta, CheckSize, CheckPriority bool

		method                       *expr.MethodExpr
		collection                   string
		entries                      *codegen.TransformPlan
		entriesSource, entriesTarget *codegen.GoTypePlan
		next                         *codegen.TransformPlan
		nextSource, nextTarget       *codegen.GoTypePlan
	}
)

// planCatalogs selects the exact endpoint already retained by common dispatch.
// Later conversion uses its selected result view rather than copying native types.
func planCatalogs(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	for _, catalog := range []struct {
		method                *expr.MethodExpr
		collection, operation string
		target                **catalogAdapter
	}{
		{prepared.mcp.ToolCatalog, "tools", "tools/list", &data.ToolCatalog},
		{prepared.mcp.PromptCatalog, "prompts", "prompts/list", &data.PromptCatalog},
		{prepared.mcp.ResourceCatalog, "resources", "resources/list", &data.ResourceCatalog},
		{prepared.mcp.ResourceTemplateCatalog, "resourceTemplates", "resources/templates/list", &data.ResourceTemplateCatalog},
	} {
		if catalog.method == nil {
			continue
		}
		if err := validateExecutionViews(catalog.method, func(result *expr.AttributeExpr) error {
			entries := result.Find(catalog.collection)
			if entries == nil {
				return fmt.Errorf("catalog view must contain %s", catalog.collection)
			}
			if catalog.collection == "resources" || catalog.collection == "resourceTemplates" {
				target := prepared.mcpService.Method(catalog.operation).Result.Find(catalog.collection)
				return checkContentFieldType(entries, target)
			}
			return nil
		}); err != nil {
			return err
		}

		if err := checkContentGoType(catalog.method.Payload); err != nil {
			return err
		}
		if err := checkContentGoType(catalog.method.Result); err != nil {
			return err
		}
		adapter := &catalogAdapter{method: catalog.method, collection: catalog.collection, Operation: catalog.operation}
		for _, endpoint := range data.EndpointMethods {
			if endpoint.method == catalog.method {
				adapter.Endpoint = endpoint
				break
			}
		}
		result := adapter.Endpoint.resultAttribute
		if result.Find(catalog.collection) == nil {
			return fmt.Errorf("MCP catalog method %q selected view omits %s", catalog.method.Name, catalog.collection)
		}
		if catalog.collection == "resources" || catalog.collection == "resourceTemplates" {
			entries := result.Find(catalog.collection)
			protocol := prepared.mcpService.Method(catalog.operation)
			target := protocol.Result.Find(catalog.collection)
			if err := checkContentFieldType(entries, target); err != nil {
				return fmt.Errorf("MCP %s catalog: %w", catalog.collection, err)
			}
			matches := adapter.Endpoint.resultLayout.PlansForOccurrence(entries)
			if len(matches) != 1 {
				return fmt.Errorf("MCP catalog entries have %d layouts", len(matches))
			}
			adapter.entriesSource = matches[0]
			layout, err := services.MethodTypeLayout(protocol, protocol.Result)
			if err != nil {
				return err
			}
			matches = layout.PlansForOccurrence(target)
			if len(matches) != 1 {
				return fmt.Errorf("MCP protocol entries have %d layouts", len(matches))
			}
			adapter.entriesTarget = matches[0]
			adapter.entries, err = codegen.NewTransformPlan(entries, target, "catalog", nil)
			if err != nil {
				return err
			}
			for i, helper := range adapter.entries.Helpers() {
				declaration := codegen.NewExactName(codegen.NameFunction, fmt.Sprintf("%sCatalogHelper%d", catalog.collection, i))
				if err := generation.Package(data.mcpImportPath).DeclareName(declaration); err != nil {
					return err
				}
				if err := adapter.entries.BindHelperDeclaration(helper.ID, declaration); err != nil {
					return err
				}
			}
			item := expr.AsArray(entries.Type).ElemType
			adapter.CheckMeta = item.Find("_meta") != nil
			adapter.CheckSize = item.Find("size") != nil
			if annotations := item.Find("annotations"); annotations != nil {
				adapter.CheckPriority = annotations.Find("priority") != nil
			}
			data.NeedsContentMeta = data.NeedsContentMeta || adapter.CheckMeta
			data.NeedsContentNumbers = data.NeedsContentNumbers || adapter.CheckSize || adapter.CheckPriority
		}

		if next := result.Find("nextCursor"); next != nil {
			matches := adapter.Endpoint.resultLayout.PlansForOccurrence(next)
			if len(matches) != 1 {
				return fmt.Errorf("MCP catalog method %q next cursor has %d layouts", catalog.method.Name, len(matches))
			}
			adapter.nextSource = matches[0]
			target := prepared.mcpService.Method(catalog.operation)
			layout, err := services.MethodTypeLayout(target, target.Result)
			if err != nil {
				return err
			}
			matches = layout.PlansForOccurrence(target.Result.Find("nextCursor"))
			if len(matches) != 1 {
				return fmt.Errorf("MCP catalog protocol next cursor has %d layouts", len(matches))
			}
			adapter.nextTarget = matches[0]
			adapter.next, err = codegen.NewTransformPlan(next, target.Result.Find("nextCursor"), "catalog", nil)
			if err != nil {
				return err
			}
		}
		*catalog.target = adapter
		data.NeedsServerCodec = true
	}
	return nil
}

// bindCatalogs resolves native selectors and typed cursor conversions after Goa
// chooses final names. The same private constructor used by other MCP methods
// validates domain input before native HTTP fields and endpoint authorization.
func bindCatalogs(services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	for _, catalog := range []*catalogAdapter{data.ToolCatalog, data.PromptCatalog, data.ResourceCatalog, data.ResourceTemplateCatalog} {
		if catalog == nil {
			continue
		}
		values := planned.methodCodecs[catalog.method.Name]
		var err error
		catalog.PayloadTransportRef, err = values.payload.TransportTypeName(data.mcpImportPath, data.mcpPackage.ImportName)
		if err != nil {
			return err
		}
		catalog.PayloadConstructor = data.CodecPackage + "." + values.payload.TransportConstructorDeclaration().Name()
		cursor := catalog.method.Payload.Find("cursor")
		catalog.Cursor, err = values.payload.TransportField(cursor, "cursor", data.mcpImportPath, data.mcpPackage.ImportName)
		if err != nil {
			return err
		}
		scope := services.ServiceAttributor(planned.prepared.userService.Name, data.mcpImportPath)
		if catalog.Endpoint.ProjectedResult || catalog.Endpoint.ExecutionView {
			scope = services.ViewAttributor(planned.prepared.userService.Name, data.mcpImportPath)
		}
		names := catalog.Endpoint.resultAttribute.Find(catalog.collection)
		catalog.EntriesField = scope.Field(names, catalog.collection, true)
		layouts := catalog.Endpoint.resultLayout.PlansForOccurrence(names)
		if len(layouts) != 1 {
			return fmt.Errorf("MCP catalog names have %d layouts", len(layouts))
		}
		catalog.NamePointer = layouts[0].Elem().IsPointer()
		if catalog.entries != nil {
			source, err := (&codegen.AttributeContext{Scope: scope, UseDefault: true, Pointer: catalog.entriesSource.Policy().Pointer}).WithGoTypeLayout(catalog.entriesSource.Link(data.mcpImportPath, data.mcpPackage.ImportName))
			if err != nil {
				return err
			}
			protocol := services.ServiceAttributor(planned.prepared.mcpService.Name, data.mcpImportPath)
			target, err := (&codegen.AttributeContext{Scope: protocol, UseDefault: true}).WithGoTypeLayout(catalog.entriesTarget.Link(data.mcpImportPath, data.mcpPackage.ImportName))
			if err != nil {
				return err
			}
			if err := catalog.entries.BindContexts(source, target); err != nil {
				return err
			}
			catalog.EntriesConversion, catalog.Helpers, err = catalog.entries.Render(catalog.Endpoint.ResultValue+"."+catalog.EntriesField, catalog.collection, true)
			if err != nil {
				return err
			}
		}

		if catalog.next == nil {
			continue
		}
		source, err := (&codegen.AttributeContext{Scope: scope, UseDefault: true, Pointer: catalog.nextSource.Policy().Pointer}).WithGoTypeLayout(catalog.nextSource.Link(data.mcpImportPath, data.mcpPackage.ImportName))
		if err != nil {
			return err
		}
		protocol := services.ServiceAttributor(planned.prepared.mcpService.Name, data.mcpImportPath)
		target, err := (&codegen.AttributeContext{Scope: protocol, UseDefault: true}).WithGoTypeLayout(catalog.nextTarget.Link(data.mcpImportPath, data.mcpPackage.ImportName))
		if err != nil {
			return err
		}
		if err := catalog.next.BindContexts(source, target); err != nil {
			return err
		}
		selector := scope.Field(catalog.Endpoint.resultAttribute.Find("nextCursor"), "nextCursor", true)
		// Goa converts scalar values. The retained field layout decides whether
		// the native cursor first needs dereferencing and whether it is absent.
		value := catalog.Endpoint.ResultValue + "." + selector
		argument := value
		if catalog.nextSource.IsPointer() {
			argument = "*" + value
		}
		conversion, _, err := catalog.next.Render(argument, "cursorValue", true)
		if err != nil {
			return err
		}
		catalog.NextCursor = "var nextCursor *" + catalog.nextTarget.Link(data.mcpImportPath, data.mcpPackage.ImportName).Ref() + "\n"
		if catalog.nextSource.IsPointer() {
			catalog.NextCursor += "if " + value + " != nil {\n" + conversion + "\nnextCursor = &cursorValue\n}"
		} else {
			catalog.NextCursor += conversion + "\nnextCursor = &cursorValue"
		}
	}
	return nil
}
