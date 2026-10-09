// Package codegen connects catalog pages and direct skill lookup to configured
// Goa endpoints. Applications own visibility, pagination and returned entries.
// Generation retains native layouts and selected views, supplies authored tool
// and prompt metadata, and converts typed entries to the protocol representation.
// Open extension values use the shared private JSON codecs.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/mcpcontract"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// discoveryAdapter retains one discovery owner and its typed input/output layouts.
	discoveryAdapter struct {
		// Endpoint invokes the configured original discovery method.
		Endpoint *endpointMethodAdapter
		// PayloadTransportRef names the private discovery input record.
		PayloadTransportRef string
		// PayloadConstructor validates discovery input and constructs the native payload.
		PayloadConstructor string
		// Input retains Goa's exact input field, alias and pointer representation.
		Input *jsoncodec.TransportField
		// InputName selects the protocol cursor or URI at generation time.
		InputName string
		// SingleEntry selects a required URI input and one complete returned entry.
		SingleEntry bool
		// Operation names the protocol method receiving native HTTP inputs.
		Operation string
		// EntriesField selects the validated native page or single entry.
		EntriesField string
		// NamePointer records whether each returned name is a pointer in this view.
		NamePointer bool
		// NextCursor copies the native optional cursor using Goa's conversion plan.
		NextCursor string
		// EntriesConversion copies the native page or entry to its protocol type.
		EntriesConversion string
		// Helpers contains Goa conversions for nested descriptor types.
		Helpers []*codegen.TransformFunctionData
		// CheckMeta, CheckSize and CheckPriority select declared metadata checks.
		CheckMeta, CheckSize, CheckPriority bool

		// Metadata selects the generated encoder for an authored extension object.
		Metadata *contentMetadataData
		// EntryEncoder supplies the shared codec for cross-field Skill verification.
		EntryEncoder string

		entryAttribute               *expr.AttributeExpr
		entryCodec                   *jsoncodec.Value
		metaAttribute                *expr.AttributeExpr
		metaLayout                   *codegen.GoTypePlan
		metaCodec                    *jsoncodec.Value
		method                       *expr.MethodExpr
		collection                   string
		entries                      *codegen.TransformPlan
		entriesSource, entriesTarget *codegen.GoTypePlan
		next                         *codegen.TransformPlan
		nextSource, nextTarget       *codegen.GoTypePlan
	}
)

// planDiscovery selects the exact endpoint already retained by common dispatch.
// Later conversion uses its selected result view rather than copying native types.
func planDiscovery(generation *codegen.Generation, services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData) error {
	for _, catalog := range []struct {
		method                       *expr.MethodExpr
		collection, operation, input string
		single                       bool
		target                       **discoveryAdapter
	}{
		{prepared.mcp.ToolCatalog, "tools", "tools/list", "cursor", false, &data.ToolCatalog},
		{prepared.mcp.PromptCatalog, "prompts", "prompts/list", "cursor", false, &data.PromptCatalog},
		{prepared.mcp.ResourceCatalog, "resources", "resources/list", "cursor", false, &data.ResourceCatalog},
		{prepared.mcp.ResourceTemplateCatalog, "resourceTemplates", "resources/templates/list", "cursor", false, &data.ResourceTemplateCatalog},
		{prepared.mcp.SkillCatalog, "skills", "skills/list", "cursor", false, &data.SkillCatalog},
		{prepared.mcp.SkillLookup, "skill", "skills/get", "uri", true, &data.SkillLookup},
	} {
		if catalog.method == nil {
			continue
		}
		if err := validateExecutionViews(catalog.method, func(result *expr.AttributeExpr) error {
			entries := result.Find(catalog.collection)
			if entries == nil {
				return fmt.Errorf("catalog view must contain %s", catalog.collection)
			}
			if catalog.collection != "tools" && catalog.collection != "prompts" {
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
		adapter := &discoveryAdapter{method: catalog.method, collection: catalog.collection, Operation: catalog.operation, InputName: catalog.input, SingleEntry: catalog.single}
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
		if catalog.collection != "tools" && catalog.collection != "prompts" {
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
			projection := entries
			item := entries
			if !catalog.single {
				item = expr.AsArray(entries.Type).ElemType
			}
			if catalog.operation == "skills/list" || catalog.operation == "skills/get" {
				adapter.entryAttribute = item
			}
			if metadata := item.Find("_meta"); metadata != nil && expr.AsObject(metadata.Type) != nil {
				adapter.metaAttribute = metadata
				selected := expr.NewAttributeGraphCopier().Copy(result)
				projection = selected.Find(catalog.collection)
				array := expr.AsArray(projection.Type)
				array.ElemType, err = mcpcontract.WithoutField(array.ElemType, "_meta")
				if err != nil {
					return err
				}
				layout, err := services.MethodTypeLayout(catalog.method, selected)
				if err != nil {
					return err
				}
				matches := layout.PlansForOccurrence(projection)
				if len(matches) != 1 {
					return fmt.Errorf("MCP catalog field projection has %d layouts", len(matches))
				}
				adapter.entriesSource = matches[0]
			}
			adapter.entries, err = codegen.NewTransformPlan(projection, target, "catalog", nil)
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
			adapter.CheckMeta = item.Find("_meta") != nil && adapter.metaAttribute == nil
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

// bindDiscovery resolves native selectors and typed cursor conversions after Goa
// chooses final names. The same private constructor used by other MCP methods
// validates domain input before native HTTP fields and endpoint authorization.
func bindDiscovery(services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	for _, catalog := range []*discoveryAdapter{data.ToolCatalog, data.PromptCatalog, data.ResourceCatalog, data.ResourceTemplateCatalog, data.SkillCatalog, data.SkillLookup} {
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
		if catalog.entryCodec != nil {
			catalog.EntryEncoder = data.CodecPackage + "." + catalog.entryCodec.EncodeDeclaration().Name()
		}
		input := catalog.method.Payload.Find(catalog.InputName)
		catalog.Input, err = values.payload.TransportField(input, catalog.InputName, data.mcpImportPath, data.mcpPackage.ImportName)
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
		if !catalog.SingleEntry {
			catalog.NamePointer = layouts[0].Elem().IsPointer()
		}
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
			if catalog.metaCodec != nil {
				catalog.Metadata = &contentMetadataData{
					Field:       scope.Field(catalog.metaAttribute, "_meta", true),
					Encode:      data.CodecPackage + "." + catalog.metaCodec.EncodeDeclaration().Name(),
					Optional:    !expr.AsArray(names.Type).ElemType.IsRequired("_meta") && catalog.metaLayout.IsPointer(),
					TargetField: protocol.Field(expr.AsArray(planned.prepared.mcpService.Method(catalog.Operation).Result.Find(catalog.collection).Type).ElemType.Find("_meta"), "_meta", true),
				}
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
