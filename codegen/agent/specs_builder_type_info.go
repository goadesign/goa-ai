// Package codegen builds each generated type together with its JSON schema, example,
// validation code, and conversion functions.
package codegen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"goa.design/goa-ai/boundedresult"
	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

const jsonSchemaTypeObject = "object"

// scopeForTool returns the names saved for the public specs package.
func (b *toolSpecBuilder) scopeForTool() *codegen.NameScope {
	return b.svcScope
}

// typeFor builds the names, Go types, schemas, and JSON functions for one tool
// input, output, or server data value.
func (b *toolSpecBuilder) typeFor(owner *contractTypeOwner, att *goaexpr.AttributeExpr, usage typeUsage) (*typeData, error) {
	// A tool's declared result controls the JSON shown to the model. When the
	// tool has no result, use the result of its Goa service method.
	//
	// Tool inputs always use the tool's own arguments. Fields supplied by the
	// server must not appear in the JSON accepted from the model.
	if owner != nil && owner.PreferMethodResult && usage == usageResult {
		if (att == nil || att.Type == goaexpr.Empty) &&
			owner.MethodResultAttr != nil && owner.MethodResultAttr.Type != goaexpr.Empty {
			att = owner.MethodResultAttr
		}
	}

	if usage == usagePayload && att.Type == goaexpr.Empty {
		// Inputs with no arguments still use a named empty object so every tool has
		// a concrete input type and JSON decoder.
		att = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}

	info, err := b.buildTypeInfo(owner, att, usage, "")
	if err != nil {
		return nil, err
	}
	return info, nil
}

// buildTypeInfo builds one named Go type and the data needed to write its JSON
// schema, decoder, encoder, and validator.
func (b *toolSpecBuilder) buildTypeInfo(owner *contractTypeOwner, att *goaexpr.AttributeExpr, usage typeUsage, qualifier string) (*typeData, error) {
	if owner == nil || owner.ScopeName == "" {
		return nil, fmt.Errorf("invalid contract metadata: missing owner scope")
	}
	// A missing type means the generator received an invalid design value.
	assertNoNilTypes(att, owner, usage, "contract-attr")
	identity := stableTypeKey(owner, usage, qualifier)
	if existing := b.contractTypes[identity]; existing != nil {
		return existing, nil
	}
	scope := b.scopeForTool()
	planned := b.planned.typeFor(owner, usage, qualifier)
	if planned == nil {
		return nil, fmt.Errorf("type %v was not planned", identity)
	}
	typeName := planned.publicDeclaration.Name()
	key := "name:" + typeName

	defineType := false
	// Public tool types keep required fields as values and apply Goa defaults.
	const (
		publicPtr      = false
		publicDefaults = true
	)
	b.materializeNestedLocalTypes(scope, planned.publicTypes, publicPtr, publicDefaults)
	tt, defLine, fullRef := b.buildTypeDefinition(typeName, planned.public, scope, defineType, publicPtr, publicDefaults)
	b.collectUnionSumTypes(scope, tt)
	ptr := planned.publicLayout.ReferenceIsPointer()

	transportAttr := planned.transportShape
	schemaAttr := cloneModelSchemaAttribute(transportAttr)

	// Only examples written by the design author are shown to the model.
	authoredExample := authoredExampleForAttribute(att)
	var example *exampleData
	if usage == usagePayload || (usage == usageResult && owner.Kind == contractTypeOwnerCompletion) {
		// Union examples use the {type,value} JSON form accepted by the decoder.
		example = authoredExample
	}

	var err error
	exampleIdentity := goaexpr.UserTypeExampleIdentity(&goaexpr.UserTypeExpr{
		TypeName: key,
		UID:      "goa-ai:tool-spec:" + key,
	})
	schemaBytes, schemaWithoutRootExampleBytes, err := schemaVariantsForAttribute(
		b.api,
		schemaAttr,
		exampleValue(example),
		exampleIdentity,
	)
	if err != nil {
		return nil, fmt.Errorf("build %s %s schema: %w", owner.QualifiedName, usage, err)
	}
	var executionSchemaBytes []byte
	if usage == usagePayload {
		executionSchemaBytes, err = specializeExecutionPayloadSchema(schemaWithoutRootExampleBytes, owner.Bounds)
		if err != nil {
			return nil, fmt.Errorf("build %s execution payload schema: %w", owner.QualifiedName, err)
		}
	}
	if usage == usagePayload && len(owner.ModelHiddenPayloadFields) > 0 {
		schemaBytes, err = removePayloadSchemaFields(schemaBytes, owner.ModelHiddenPayloadFields)
		if err != nil {
			return nil, err
		}
		schemaWithoutRootExampleBytes, err = removePayloadSchemaFields(
			schemaWithoutRootExampleBytes,
			owner.ModelHiddenPayloadFields,
		)
		if err != nil {
			return nil, err
		}
	}
	if usage == usageResult && owner.Bounds != nil {
		schemaBytes, err = projectBoundedResultSchema(schemaBytes, owner.Bounds)
		if err != nil {
			return nil, err
		}
		schemaWithoutRootExampleBytes, err = projectBoundedResultSchema(schemaWithoutRootExampleBytes, owner.Bounds)
		if err != nil {
			return nil, err
		}
	}
	exampleBytes, err := modelVisibleExampleJSON(example, owner, usage)
	if err != nil {
		return nil, err
	}

	doc := fmt.Sprintf("%s defines the JSON %s for the %s tool.", typeName, usage, owner.QualifiedName)
	if owner.Kind == contractTypeOwnerCompletion {
		doc = fmt.Sprintf("%s defines the JSON %s for the completion %s.", typeName, usage, owner.QualifiedName)
	}
	dst := planned.public
	var (
		transportTypeName      string
		transportDef           string
		transportValidationSrc []string
		transportTypeRef       string
		transportPointer       bool
		transportValidator     string
		decodeBody             string
		encodeBody             string
	)
	if planned.transportDeclaration != nil {
		// The HTTP type carries JSON field names and pointer fields needed to
		// distinguish missing values from zero values.
		transportScope := b.transportScope
		b.materializeNestedTransportTypes(transportScope, planned.transportTypes)
		b.collectTransportUnionSumTypes(transportScope, transportAttr)
		transportTypeName = planned.transportDeclaration.Name()
		transportCtx := modelJSONTransportContext(transportScope, true, "")
		transportDef = transportTypeName + " " + transportTypeDef(transportScope, transportAttr, transportCtx)
		httpctx := modelJSONTransportContext(transportScope, !goaexpr.IsPrimitive(schemaAttr.Type), "")
		transportValidation := validationCodeWithContext(schemaAttr, nil, httpctx, true, false, false, "body", owner, usage, "transport")
		if strings.TrimSpace(transportValidation) != "" {
			transportValidationSrc = strings.Split(transportValidation, "\n")
		}

		src := planned.transport
		srcCtx := modelJSONTransportContext(transportScope, true, "toolhttp")
		tgtCtx := codegen.NewAttributeContext(false, false, true, "", scope)
		encSrcCtx := codegen.NewAttributeContext(false, false, true, "", scope)
		encTgtCtx := modelJSONTransportContext(transportScope, true, "toolhttp")
		outputPath := b.publicPackage.ImportPath()
		importName := b.publicPackage.ImportName
		srcCtx, err = srcCtx.WithGoTypeLayout(planned.transportLayout.Link(outputPath, importName))
		if err != nil {
			return nil, err
		}
		tgtCtx, err = tgtCtx.WithGoTypeLayout(planned.publicLayout.Link(outputPath, importName))
		if err != nil {
			return nil, err
		}
		encSrcCtx, err = encSrcCtx.WithGoTypeLayout(planned.publicLayout.Link(outputPath, importName))
		if err != nil {
			return nil, err
		}
		encTgtCtx, err = encTgtCtx.WithGoTypeLayout(planned.transportLayout.Link(outputPath, importName))
		if err != nil {
			return nil, err
		}
		if err := planned.decode.BindContexts(srcCtx, tgtCtx); err != nil {
			return nil, err
		}
		var decodeHelpers []*codegen.TransformFunctionData
		decodeBody, decodeHelpers, err = planned.decode.Render("in", "out", false)
		if err != nil {
			return nil, err
		}
		if err := planned.encode.BindContexts(encSrcCtx, encTgtCtx); err != nil {
			return nil, err
		}
		var encodeHelpers []*codegen.TransformFunctionData
		encodeBody, encodeHelpers, err = planned.encode.Render("in", "out", false)
		if err != nil {
			return nil, err
		}
		b.codecTransformHelpers = codegen.AppendHelpers(b.codecTransformHelpers, decodeHelpers)
		b.codecTransformHelpers = codegen.AppendHelpers(b.codecTransformHelpers, encodeHelpers)
		transportTypeRef = transportScope.GoTypeRef(src)
		transportPointer = planned.transportLayout.ReferenceIsPointer()
		transportValidator = planned.transportValidator.Name()
	}
	jsonValidatorFunc, jsonValueValidatorFunc := materializeJSONValidatorGraph(planned.jsonValidator)
	info := &typeData{
		Key:                          key,
		TypeName:                     typeName,
		Doc:                          doc,
		Def:                          defLine,
		SchemaJSON:                   schemaBytes,
		ExecutionSchemaJSON:          executionSchemaBytes,
		SchemaWithoutRootExampleJSON: schemaWithoutRootExampleBytes,
		ExampleJSON:                  exampleBytes,
		ScaffoldExampleJSON:          exampleJSON(authoredExample),
		ExportedCodec:                planned.exportedCodec.Name(),
		GenericCodec:                 planned.genericCodec.Name(),
		MarshalFunc:                  planned.marshal.Name(),
		UnmarshalFunc:                planned.unmarshal.Name(),
		ValidateFunc:                 transportValidator,
		FieldsVar:                    planned.fieldMetadata.Name(),
		JSONValidatorFunc:            jsonValidatorFunc,
		JSONValueValidatorFunc:       jsonValueValidatorFunc,
		EnrichValidationFunc:         planned.enrichValidation.Name(),
		InvalidFieldTypeFunc:         planned.invalidFieldType.Name(),
		FullRef:                      fullRef,
		NeedType:                     defLine != "",
		IsToolType:                   usage == usagePayload || usage == usageResult || usage == usageServerData,
		PublicType:                   dst,
		NilError:                     fmt.Sprintf("%s is nil", lowerCamel(typeName)),
		DecodeError:                  fmt.Sprintf("decode %s", lowerCamel(typeName)),
		ValidateError:                fmt.Sprintf("validate %s", lowerCamel(typeName)),
		EmptyError:                   fmt.Sprintf("%s JSON is empty", lowerCamel(typeName)),
		Usage:                        usage,
		GenerateCodec:                true,
		Pointer:                      ptr,
		MarshalArg:                   "v",
		UnmarshalArg:                 "v",
		TransportTypeName:            transportTypeName,
		TransportDef:                 transportDef,
		TransportValidationSrc:       transportValidationSrc,
		TransportTypeRef:             transportTypeRef,
		TransportPointer:             transportPointer,
		DecodeTransform:              decodeBody,
		EncodeTransform:              encodeBody,
	}
	// Accept empty JSON for payloads that are empty structs (no fields).
	if usage == usagePayload && isEmptyStruct(att) {
		info.AcceptEmpty = true
	}
	if fields := buildFieldMetadata(schemaAttr); len(fields) > 0 {
		info.Fields = deleteModelHiddenFields(fields, owner, usage)
	}
	b.contractTypes[identity] = info
	// Also index by Go name so the same type is not written twice.
	nameKey := "name:" + typeName
	if _, exists := b.types[nameKey]; !exists {
		b.types[nameKey] = info
	}
	return info, nil
}

// typeFor returns the saved type record for one tool or completion value.
func (p *toolSpecsPackagePlan) typeFor(owner *contractTypeOwner, usage typeUsage, qualifier string) *plannedSpecType {
	if owner.Kind == contractTypeOwnerCompletion {
		return p.completionNames[owner.Name].resultType
	}
	names := p.tools[owner.Name]
	switch usage {
	case usagePayload:
		return names.payloadType
	case usageResult:
		return names.resultType
	case usageServerData:
		return names.serverDataTypes[qualifier]
	default:
		panic(fmt.Sprintf("unknown type use %q", usage))
	}
}

// specializeExecutionPayloadSchema gives each dedicated paging tool the exact
// payload it receives from continuation handling. The first tool cannot accept
// a cursor. Its continuation requires one.
func specializeExecutionPayloadSchema(schemaBytes []byte, bounds *ToolBoundsData) ([]byte, error) {
	if bounds == nil || bounds.Paging == nil || bounds.Paging.ContinueTool == "" {
		return append([]byte(nil), schemaBytes...), nil
	}
	paging := bounds.Paging
	if paging.SourceTool == "" {
		return removePayloadSchemaFields(schemaBytes, []string{paging.CursorField})
	}

	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return nil, fmt.Errorf("decode payload schema: %w", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties[paging.CursorField] == nil {
		return nil, fmt.Errorf("continuation payload schema is missing cursor field %q", paging.CursorField)
	}
	required, _ := schema["required"].([]any)
	for _, item := range required {
		if name, ok := item.(string); ok && name == paging.CursorField {
			return append([]byte(nil), schemaBytes...), nil
		}
	}
	schema["required"] = append(required, paging.CursorField)
	projected, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode payload schema: %w", err)
	}
	return projected, nil
}

// removePayloadSchemaFields removes root fields from a generated JSON schema.
func removePayloadSchemaFields(schemaBytes []byte, fields []string) ([]byte, error) {
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return nil, fmt.Errorf("decode payload schema: %w", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, field := range fields {
		delete(properties, modelJSONName(field))
	}
	if len(properties) == 0 {
		delete(schema, "properties")
		delete(schema, "$defs")
	}
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, item := range required {
			name, _ := item.(string)
			if !containsModelField(fields, name) {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(schema, "required")
		} else {
			schema["required"] = kept
		}
	}
	if rootExample, ok := schema["example"].(map[string]any); ok {
		for _, field := range fields {
			delete(rootExample, modelJSONName(field))
		}
	}
	projected, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode payload schema: %w", err)
	}
	return projected, nil
}

// modelVisibleExampleJSON removes runtime-supplied fields from the standalone
// example document advertised to models.
func modelVisibleExampleJSON(example *exampleData, owner *contractTypeOwner, usage typeUsage) ([]byte, error) {
	encoded := exampleJSON(example)
	if usage != usagePayload || len(owner.ModelHiddenPayloadFields) == 0 || len(encoded) == 0 {
		return encoded, nil
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, fmt.Errorf("decode authored model payload example: %w", err)
	}
	for _, field := range owner.ModelHiddenPayloadFields {
		delete(value, modelJSONName(field))
	}
	projected, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode authored model payload example: %w", err)
	}
	return projected, nil
}

// deleteModelHiddenFields removes runtime-supplied root fields from generated
// metadata before it is advertised to the model.
func deleteModelHiddenFields(fields []*fieldMetadataData, owner *contractTypeOwner, usage typeUsage) []*fieldMetadataData {
	if usage != usagePayload {
		return fields
	}
	kept := fields[:0]
	for _, field := range fields {
		if !isModelHiddenPath(field.Path, owner.ModelHiddenPayloadFields) {
			kept = append(kept, field)
		}
	}
	return kept
}

// isModelHiddenPath reports whether a metadata path names a runtime-supplied
// root field or one of its descendants.
func isModelHiddenPath(path []fieldPathSegmentData, hidden []string) bool {
	if len(path) == 0 || path[0].Dynamic {
		return false
	}
	for _, field := range hidden {
		if path[0].Name == modelJSONName(field) {
			return true
		}
	}
	return false
}

// containsModelField reports whether modelName is the JSON name of one of the
// design fields.
func containsModelField(fields []string, modelName string) bool {
	for _, field := range fields {
		if modelJSONName(field) == modelName {
			return true
		}
	}
	return false
}

// NOTE: The reserved `server_data` payload field is added by the runtime, not by
// the generated tool schema. Tool payload schemas remain stable and do not
// include runtime-reserved controls.

func projectBoundedResultSchema(schemaBytes []byte, bounds *ToolBoundsData) ([]byte, error) {
	if len(schemaBytes) == 0 || bounds == nil {
		return schemaBytes, nil
	}

	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return nil, fmt.Errorf("unmarshal bounded result schema: %w", err)
	}
	if schemaType, ok := schema["type"].(string); ok && schemaType != jsonSchemaTypeObject {
		return nil, fmt.Errorf("bounded tool result schema must be an object, got %q", schemaType)
	}
	schema["type"] = jsonSchemaTypeObject

	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties == nil {
		properties = make(map[string]any)
		schema["properties"] = properties
	}
	for name, fieldSchema := range boundedResultSchemaFields(bounds) {
		properties[name] = fieldSchema
	}
	schema["required"] = mergeBoundedResultRequired(schema["required"], bounds, boundedresult.RequiredFieldNames()...)

	projected, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal bounded result schema: %w", err)
	}
	return projected, nil
}

func boundedResultSchemaFields(bounds *ToolBoundsData) map[string]any {
	fields := map[string]any{
		boundedresult.FieldReturned: map[string]any{
			"type":        jsonSchemaTypeInteger,
			"description": "Number of items returned in this response after applying tool limits.",
		},
		boundedresult.FieldTotal: map[string]any{
			"type":        jsonSchemaTypeInteger,
			"description": "Total number of matching items before truncation.",
		},
		boundedresult.FieldTruncated: map[string]any{
			"type":        "boolean",
			"description": "True when this result is partial because tool limits or caps were applied.",
		},
		boundedresult.FieldRefinementHint: map[string]any{
			"type":        "string",
			"description": "Short guidance on how to narrow the request when the result is truncated.",
		},
	}
	if field := modelVisibleNextCursorField(bounds); field != "" {
		fields[modelJSONName(field)] = map[string]any{
			"type":        "string",
			"description": "Continuation reference for the next page.",
		}
	}
	return fields
}

// mergeBoundedResultRequired preserves authored required fields while forcing
// the canonical bounded contract: returned/truncated are required and the
// remaining bounded fields stay optional.
func mergeBoundedResultRequired(existing any, bounds *ToolBoundsData, names ...string) []any {
	requiredSet := make(map[string]struct{}, len(names))
	for _, name := range names {
		requiredSet[name] = struct{}{}
	}
	if boundsTotalRequired(bounds) {
		requiredSet[boundedresult.FieldTotal] = struct{}{}
	}
	optionalBoundsFields := canonicalOptionalBoundedResultFields(bounds)
	if existingRequired, ok := existing.([]any); ok {
		for _, item := range existingRequired {
			if name, ok := item.(string); ok && name != "" {
				if _, isOptionalBound := optionalBoundsFields[name]; isOptionalBound {
					continue
				}
				requiredSet[name] = struct{}{}
			}
		}
	}
	merged := make([]string, 0, len(requiredSet))
	for name := range requiredSet {
		merged = append(merged, name)
	}
	sort.Strings(merged)

	out := make([]any, 0, len(merged))
	for _, name := range merged {
		out = append(out, name)
	}
	return out
}

// canonicalOptionalBoundedResultFields returns the bounded-result fields that
// must remain optional in the generated JSON schema.
func canonicalOptionalBoundedResultFields(bounds *ToolBoundsData) map[string]struct{} {
	nextCursorField := modelVisibleNextCursorField(bounds)
	fields := make(map[string]struct{})
	for _, name := range boundedresult.OptionalFieldNames(nextCursorField) {
		if name == boundedresult.FieldTotal && boundsTotalRequired(bounds) {
			continue
		}
		fields[modelJSONName(name)] = struct{}{}
	}
	return fields
}

// boundsTotalRequired reports whether the bound method guarantees exact total
// cardinality for every successful result.
func boundsTotalRequired(bounds *ToolBoundsData) bool {
	return bounds != nil &&
		bounds.Projection != nil &&
		bounds.Projection.Total != nil &&
		bounds.Projection.Total.Required
}

// modelVisibleNextCursorField returns the cursor only for legacy same-tool
// paging. Dedicated continuation tools keep cursor state in the runtime.
func modelVisibleNextCursorField(bounds *ToolBoundsData) string {
	if bounds == nil || bounds.Paging == nil || bounds.Paging.ContinueTool != "" {
		return ""
	}
	return bounds.Paging.NextCursorField
}

// isEmptyStruct reports whether the provided attribute ultimately resolves to
// an object with no fields (empty struct). It follows user type aliases to
// inspect the underlying attribute graph.
