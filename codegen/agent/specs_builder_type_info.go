package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"goa.design/goa-ai/boundedresult"
	"goa.design/goa-ai/codegen/shared"
	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

const jsonSchemaTypeObject = "object"

// scopeForTool returns the NameScope used to derive all type and helper names
// for the specs package being generated.
func (b *toolSpecBuilder) scopeForTool() *codegen.NameScope {
	// Always use the builder scope so naming is stable for this generated package
	// across generator passes.
	if b == nil || b.svcScope == nil {
		panic("agent/specs_builder: nil toolSpecBuilder scope")
	}
	return b.svcScope
}

// typeFor returns type metadata for a contract payload/result/sidecar attribute,
// applying owner-specific shape selection rules (e.g., method-backed result
// selection) before materialization.
func (b *toolSpecBuilder) typeFor(owner *contractTypeOwner, att *goaexpr.AttributeExpr, usage typeUsage) (*typeData, error) {
	// For method-backed tools, prefer the tool Return type for RESULTs when it
	// is explicitly declared in the DSL so that model-facing schemas reflect
	// the tool contract (e.g., AtlasListDevicesToolReturn). When no Return is
	// provided, fall back to the bound service method result type so specs
	// alias the concrete service result directly.
	//
	// For PAYLOADs, always use the tool's own argument type to prevent
	// server-only fields (e.g., session_id) from leaking into tool-visible
	// schemas. Server fields are injected post-decode by adapters before
	// making the actual service method call.
	if owner != nil && owner.PreferMethodResult && usage == usageResult {
		if (att == nil || att.Type == goaexpr.Empty) &&
			owner.MethodResultAttr != nil && owner.MethodResultAttr.Type != goaexpr.Empty {
			att = owner.MethodResultAttr
		}
	}

	if usage == usagePayload && att.Type == goaexpr.Empty {
		// For payloads with no arguments, synthesize an empty object.
		// This ensures a concrete Payload type is always generated for adapters and codecs,
		// avoiding nil checks in generated code.
		att = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}

	info, err := b.buildTypeInfo(owner, att, usage, "")
	if err != nil {
		return nil, err
	}
	return info, nil
}

// buildTypeInfo generates a standalone type definition and metadata for a
// contract payload/result/sidecar shape using a simplified materialization policy.
func (b *toolSpecBuilder) buildTypeInfo(owner *contractTypeOwner, att *goaexpr.AttributeExpr, usage typeUsage, qualifier string) (*typeData, error) {
	if owner == nil || owner.ScopeName == "" {
		return nil, fmt.Errorf("invalid contract metadata: missing owner scope")
	}
	// Enforce core invariants early: attributes must have a non-nil Type and
	// user types must always carry a non-nil AttributeExpr. Violations are
	// treated as generator bugs and must be fixed at the construction site.
	assertNoNilTypes(att, owner, usage, "contract-attr")
	typeName := codegen.Goify(owner.Name, true)
	switch usage {
	case usagePayload:
		typeName += "Payload"
	case usageResult:
		typeName += "Result"
	case usageSidecar:
		if qualifier != "" {
			typeName += codegen.Goify(qualifier, true)
		}
		typeName += "ServerData"
	}

	scope := b.scopeForTool()
	// Reserve the tool-facing type name using HashedUnique on a synthetic local
	// user type that represents this tool type. This does two things:
	//   1. It seeds the scope so later nested HashedUnique calls disambiguate
	//      colliding service/local user type names (e.g. GetTimeSeriesResult).
	//   2. It ensures subsequent transform generation (GoTransform) that uses
	//      the same synthetic user type does NOT rename it to "<TypeName>2"
	//      due to prior reservations.
	//
	// We intentionally do NOT bind design user types (like a shared Doc type)
	// to the tool-facing name, because payload and result may both alias the
	// same underlying type hash but must remain distinct tool types.
	baseAttr := att
	if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
		baseAttr = ut.Attribute()
	}
	if baseAttr.Type == goaexpr.Empty {
		baseAttr = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}
	toolUT := &goaexpr.UserTypeExpr{
		AttributeExpr: stripStructPkgMeta(baseAttr),
		TypeName:      typeName,
	}
	typeName = scope.HashedUnique(toolUT, typeName)
	toolUT.TypeName = typeName

	// Stable cache key: reference for service-alias, otherwise deterministic name
	key := stableTypeKey(owner, usage, qualifier)
	if existing := b.types[key]; existing != nil {
		return existing, nil
	}

	// Preserve user types so codecs reference service user types explicitly
	// (e.g., *alpha.Doc) even for non-method-backed tools. This ensures
	// deterministic aliasing and imports and matches the repository tests
	// which assert service-qualified references in generated codecs.
	//
	defineType := false
	// Materialize the PUBLIC tool-facing type as a service-level shape:
	// required fields are non-pointers; defaults behave normally.
	const (
		publicPtr      = false
		publicDefaults = true
	)
	att = b.ensureNestedLocalTypes(scope, att, publicPtr, publicDefaults)
	tt, defLine, fullRef, imports := b.materialize(typeName, att, scope, defineType, publicPtr, publicDefaults)
	// Collect any union sum types referenced by this tool type so the toolset
	// package can emit their definitions once.
	b.collectUnionSumTypes(scope, tt)
	// Determine pointer semantics for top-level alias/value.
	aliasIsPointer := strings.Contains(defLine, "= *")
	ptr := aliasIsPointer || strings.HasPrefix(fullRef, "*")
	// Payloads and object-shaped results/sidecars use pointer codecs so decode
	// paths can validate the tagged transport shape before transforming into the
	// public local type.
	if usage == usagePayload {
		ptr = true
	}
	if (usage == usageResult || usage == usageSidecar) && goaexpr.AsObject(baseAttr.Type) != nil {
		ptr = true
	}

	// Internal transport type used only by codecs for JSON decode+validation.
	// This is the actual JSON contract (schema property names + missing detection).
	transportTypeName := typeName + "Transport"
	transportAttr := cloneWithJSONTags(tt)
	transportAttr = b.ensureNestedLocalTransportTypes(scope, transportAttr)
	// Collect union sum types as they appear in the transport graph (after
	// localization). These are emitted into the toolset-local http package.
	b.collectTransportUnionSumTypes(scope, transportAttr)

	// JSON schema from transport attribute
	schemaAttr := transportAttr
	var err error
	schemaBytes, err := schemaForAttribute(schemaAttr)
	if err != nil {
		return nil, err
	}
	if usage == usageResult && owner.Bounds != nil {
		schemaBytes, err = projectBoundedResultSchema(schemaBytes, owner.Bounds)
		if err != nil {
			return nil, err
		}
	}

	// Example JSON for externally visible request/response contracts. Payload
	// examples guide callers toward schema-compliant inputs, while completion
	// result examples drive generated example scaffolding without inventing
	// ad-hoc sample payloads in templates.
	var exampleBytes []byte
	if usage == usagePayload || (usage == usageResult && owner.Kind == contractTypeOwnerCompletion) {
		// Examples must reflect the JSON wire contract, not the public tool type.
		// In particular, unions are encoded as canonical {type,value} objects in
		// the transport graph; deriving examples from the public type produces a
		// flattened shape that misleads callers and generated examples.
		exampleBytes = exampleForAttribute(schemaAttr)
	}

	doc := fmt.Sprintf("%s defines the JSON %s for the %s tool.", typeName, usage, owner.QualifiedName)
	if owner.Kind == contractTypeOwnerCompletion {
		doc = fmt.Sprintf("%s defines the JSON %s for the completion %s.", typeName, usage, owner.QualifiedName)
	}
	transportDef := transportTypeName + " " + scope.GoTypeDef(schemaAttr, true, false)
	transportImports := shared.GatherAttributeImports(b.genpkg, schemaAttr)
	httpctx := codegen.NewAttributeContext(!goaexpr.IsPrimitive(schemaAttr.Type), false, false, "", scope)
	transportValidation := validationCodeWithContext(schemaAttr, nil, httpctx, true, false, false, "body", owner, usage, "transport")
	var transportValidationSrc []string
	if strings.TrimSpace(transportValidation) != "" {
		transportValidationSrc = strings.Split(transportValidation, "\n")
	}

	src := &goaexpr.AttributeExpr{
		Type: &goaexpr.UserTypeExpr{
			AttributeExpr: schemaAttr,
			TypeName:      transportTypeName,
		},
	}
	dst := &goaexpr.AttributeExpr{
		Type: &goaexpr.UserTypeExpr{
			// Public tool types are local to the specs package: strip any root
			// struct:pkg:* locator metadata inherited from the *source* design type.
			// This is a shallow operation: nested shared types (e.g. gen/types) keep
			// their locators and remain qualified correctly.
			AttributeExpr: stripStructPkgMeta(tt),
			TypeName:      typeName,
		},
	}
	srcCtx := codegen.NewAttributeContextForConversion(true, false, false, "toolhttp", scope)
	tgtCtx := codegen.NewAttributeContextForConversion(false, false, true, "", scope)
	decodeBody, decodeHelpers, err := codegen.GoTransform(src, dst, "in", "out", srcCtx, tgtCtx, "decode", false)
	if err != nil {
		return nil, err
	}
	encSrcCtx := codegen.NewAttributeContextForConversion(false, false, true, "", scope)
	encTgtCtx := codegen.NewAttributeContextForConversion(true, false, false, "toolhttp", scope)
	encodeBody, encodeHelpers, err := codegen.GoTransform(dst, src, "in", "out", encSrcCtx, encTgtCtx, "encode", false)
	if err != nil {
		return nil, err
	}
	for _, h := range append(decodeHelpers, encodeHelpers...) {
		if h == nil {
			continue
		}
		key := h.Name + "|" + h.ParamTypeRef + "|" + h.ResultTypeRef
		if _, ok := b.codecTransformHelperKeys[key]; ok {
			continue
		}
		b.codecTransformHelperKeys[key] = struct{}{}
		b.codecTransformHelpers = append(b.codecTransformHelpers, h)
	}
	emitTransport := ptr || owner.Kind == contractTypeOwnerTool
	transportTypeNameOut := ""
	transportDefOut := ""
	var transportImportsOut []*codegen.ImportSpec
	var transportValidationSrcOut []string
	transportTypeRefOut := ""
	transportPointerOut := false
	if emitTransport {
		transportTypeNameOut = transportTypeName
		transportDefOut = transportDef
		transportImportsOut = transportImports
		transportValidationSrcOut = transportValidationSrc
		transportTypeRefOut = scope.GoTypeRef(src)
		transportPointerOut = strings.HasPrefix(scope.GoTypeRef(src), "*")
	}
	info := &typeData{
		Key:                    key,
		TypeName:               typeName,
		Doc:                    doc,
		Def:                    defLine,
		SchemaJSON:             schemaBytes,
		ExampleJSON:            exampleBytes,
		ExportedCodec:          typeName + "Codec",
		GenericCodec:           lowerCamel(typeName) + "Codec",
		MarshalFunc:            "Marshal" + typeName,
		UnmarshalFunc:          "Unmarshal" + typeName,
		ValidateFunc:           "",
		FullRef:                fullRef,
		NeedType:               defLine != "",
		IsToolType:             usage == usagePayload || usage == usageResult || usage == usageSidecar,
		PublicType:             dst,
		NilError:               fmt.Sprintf("%s is nil", lowerCamel(typeName)),
		DecodeError:            fmt.Sprintf("decode %s", lowerCamel(typeName)),
		ValidateError:          fmt.Sprintf("validate %s", lowerCamel(typeName)),
		EmptyError:             fmt.Sprintf("%s JSON is empty", lowerCamel(typeName)),
		Usage:                  usage,
		TypeImports:            imports,
		GenerateCodec:          true,
		Pointer:                ptr,
		MarshalArg:             "v",
		UnmarshalArg:           "v",
		TransportTypeName:      transportTypeNameOut,
		TransportDef:           transportDefOut,
		TransportImports:       transportImportsOut,
		TransportValidationSrc: transportValidationSrcOut,
		TransportTypeRef:       transportTypeRefOut,
		TransportPointer:       transportPointerOut,
		DecodeTransform:        decodeBody,
		EncodeTransform:        encodeBody,
	}
	if len(exampleBytes) > 0 && (usage == usagePayload || (usage == usageResult && owner.Kind == contractTypeOwnerCompletion)) {
		if eg, ok := exampleInputGoExpr(exampleBytes); ok {
			info.ExampleInputGo = eg
		}
	}
	// Validation is performed on the internal transport type during Unmarshal.
	// Accept empty JSON for payloads that are empty structs (no fields).
	if usage == usagePayload && isEmptyStruct(att) {
		info.AcceptEmpty = true
	}
	// Keep field descriptions for validation error enrichment.
	if fdesc := buildFieldDescriptions(schemaAttr); len(fdesc) > 0 {
		info.FieldDescs = fdesc
	}
	b.types[key] = info
	// Also index by the public type name so auxiliary passes (e.g.,
	// validator collection) can detect that a concrete alias already
	// exists and avoid emitting duplicate helpers for the same name.
	nameKey := "name:" + typeName
	if _, exists := b.types[nameKey]; !exists {
		b.types[nameKey] = info
	}
	return info, nil
}

// NOTE: The reserved `server_data` payload field is added by the runtime, not by
// the generated tool schema. Tool payload schemas remain stable and do not
// include runtime-reserved controls.

func exampleInputGoExpr(exampleJSON []byte) (string, bool) {
	trimmed := bytes.TrimSpace(exampleJSON)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return "", false
	}
	return goLiteralForAny(m), true
}

func goLiteralForAny(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case bool:
		// Keep primitive formatting aligned with Goa's codegen helpers which
		// use fmt's Go-syntax formatting (%#v) when emitting literals (see
		// goa.design/goa/v3/codegen/validation.go:toSlice).
		return fmt.Sprintf("%#v", x)
	case string:
		return fmt.Sprintf("%#v", x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return fmt.Sprintf("%#v", i)
		}
		if f, err := x.Float64(); err == nil {
			return fmt.Sprintf("%#v", f)
		}
		return fmt.Sprintf("%#v", x.String())
	case float64:
		return fmt.Sprintf("%#v", x)
	case []any:
		if len(x) == 0 {
			return "[]any{}"
		}
		elems := make([]string, len(x))
		for i, v := range x {
			elems[i] = goLiteralForAny(v)
		}
		return fmt.Sprintf("[]any{%s}", strings.Join(elems, ", "))
	case map[string]any:
		if len(x) == 0 {
			return "map[string]any{}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("map[string]any{")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%#v", k)
			b.WriteString(": ")
			b.WriteString(goLiteralForAny(x[k]))
		}
		b.WriteString("}")
		return b.String()
	default:
		// Best-effort: stringify unknown decoded values.
		return strconv.Quote(fmt.Sprintf("%v", x))
	}
}

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
			"type":        "integer",
			"description": "Number of items returned in this response after applying tool limits.",
		},
		boundedresult.FieldTotal: map[string]any{
			"type":        "integer",
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
	if bounds.Paging != nil && bounds.Paging.NextCursorField != "" {
		fields[bounds.Paging.NextCursorField] = map[string]any{
			"type":        "string",
			"description": "Opaque cursor for the next page. Call the same tool again with the same parameters and pass this exact string back as the paging cursor. Do not send the literal text \"next_cursor\" or modify the cursor.",
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
	nextCursorField := ""
	if bounds != nil && bounds.Paging != nil {
		nextCursorField = bounds.Paging.NextCursorField
	}
	fields := make(map[string]struct{})
	for _, name := range boundedresult.OptionalFieldNames(nextCursorField) {
		fields[name] = struct{}{}
	}
	return fields
}

// isEmptyStruct reports whether the provided attribute ultimately resolves to
// an object with no fields (empty struct). It follows user type aliases to
// inspect the underlying attribute graph.
