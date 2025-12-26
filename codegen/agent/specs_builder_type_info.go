package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	runtimetools "goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

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

// typeFor returns type metadata for a tool payload/result/sidecar attribute,
// applying tool-specific shape selection rules (e.g., method-backed result
// selection) before materialization.
func (b *toolSpecBuilder) typeFor(tool *ToolData, att *goaexpr.AttributeExpr, usage typeUsage) (*typeData, error) {
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
	if tool != nil && tool.IsMethodBacked && usage == usageResult {
		if (tool.Return == nil || tool.Return.Type == goaexpr.Empty) &&
			tool.MethodResultAttr != nil && tool.MethodResultAttr.Type != goaexpr.Empty {
			att = tool.MethodResultAttr
		}
	}

	if usage == usagePayload && att.Type == goaexpr.Empty {
		// For payloads with no arguments, synthesize an empty object.
		// This ensures a concrete Payload type is always generated for adapters and codecs,
		// avoiding nil checks in generated code.
		att = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}

	info, err := b.buildTypeInfo(tool, att, usage)
	if err != nil {
		return nil, err
	}
	return info, nil
}

// ensureType generates a standalone type definition and metadata for a tool's
// payload or result using a simplified, mode-driven materialization policy.
func (b *toolSpecBuilder) buildTypeInfo(tool *ToolData, att *goaexpr.AttributeExpr, usage typeUsage) (*typeData, error) {
	if tool == nil || tool.Toolset == nil {
		return nil, fmt.Errorf("invalid tool metadata: nil tool or toolset")
	}
	// For bounded tool results, extend the effective attribute with a canonical
	// bounds field. This ensures:
	//
	//   - JSON schemas and tool_schemas.json expose a standard "bounds" property.
	//   - Generated result alias types include a Bounds helper field with
	//     non-pointer Returned/Truncated fields and optional Total/RefinementHint,
	//     which in turn enables a simple, uniform implementation of
	//     agent.BoundedResult.
	if usage == usageResult && tool.BoundedResult && att != nil && att.Type != goaexpr.Empty {
		if obj := goaexpr.AsObject(att.Type); obj != nil {
			// Avoid mutating the shared design expression; work on a shallow copy of
			// the attribute and its object.
			dup := *att
			// Synthesize an attribute for the canonical bounds metadata. The
			// underlying schema is a small object with required returned/truncated
			// and optional total/refinement_hint fields so the generated helper
			// struct uses non-pointer fields for required data.
			boundsAttr := &goaexpr.AttributeExpr{
				Type: &goaexpr.Object{
					&goaexpr.NamedAttributeExpr{
						Name:      "returned",
						Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Int},
					},
					&goaexpr.NamedAttributeExpr{
						Name:      "total",
						Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Int},
					},
					&goaexpr.NamedAttributeExpr{
						Name:      "truncated",
						Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Boolean},
					},
					&goaexpr.NamedAttributeExpr{
						Name:      "refinement_hint",
						Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String},
					},
				},
				Validation: &goaexpr.ValidationExpr{
					Required: []string{"returned", "truncated"},
				},
			}
			boundsObj := make(goaexpr.Object, 0, len(*obj)+1)
			boundsObj = append(boundsObj, *obj...)
			boundsObj = append(boundsObj, &goaexpr.NamedAttributeExpr{
				Name:      "bounds",
				Attribute: boundsAttr,
			})
			dup.Type = &boundsObj
			att = &dup
		}
	}
	// Enforce core invariants early: attributes must have a non-nil Type and
	// user types must always carry a non-nil AttributeExpr. Violations are
	// treated as generator bugs and must be fixed at the construction site.
	assertNoNilTypes(att, tool, usage, "tool-attr")
	typeName := codegen.Goify(tool.Name, true)
	switch usage {
	case usagePayload:
		typeName += "Payload"
	case usageResult:
		typeName += "Result"
	case usageSidecar:
		typeName += "Sidecar"
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
	att = b.renameCollidingNestedUserTypes(att, scope)
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
	key := stableTypeKey(tool, usage)
	if existing := b.types[key]; existing != nil {
		return existing, nil
	}

	// Preserve user types so codecs reference service user types explicitly
	// (e.g., *alpha.Doc) even for non-method-backed tools. This ensures
	// deterministic aliasing and imports and matches the repository tests
	// which assert service-qualified references in generated codecs.
	//
	// For bounded tool RESULTS, we intentionally materialize a concrete
	// defined type (rather than an alias) so that codegen can attach the
	// agent.BoundedResult interface via a method on the result type.
	defineType := usage == usageResult && tool.BoundedResult
	// Materialize definition and type reference
	tt, defLine, fullRef, imports := b.materialize(typeName, att, scope, defineType)
	// Collect any union sum types referenced by this tool type so the toolset
	// package can emit their definitions once.
	b.collectUnionSumTypes(scope, tt)
	// Determine pointer semantics for top-level alias/value.
	aliasIsPointer := strings.Contains(defLine, "= *")
	ptr := aliasIsPointer || strings.HasPrefix(fullRef, "*")

	// JSON schema from effective attribute
	schemaAttr := tt
	var err error
	if usage == usagePayload && tool.Artifact != nil && tool.Artifact.Type != goaexpr.Empty {
		schemaAttr, err = schemaAttributeWithArtifactsToggle(tool, tt)
		if err != nil {
			return nil, err
		}
	}
	schemaBytes, err := schemaForAttribute(schemaAttr)
	if err != nil {
		return nil, err
	}

	// Example JSON for payload types (optional). We intentionally derive examples
	// only for payloads so runtimes can guide callers toward schema-compliant
	// inputs when decode fails.
	var exampleBytes []byte
	if usage == usagePayload {
		exampleBytes = exampleForAttribute(att)
	}

	doc := fmt.Sprintf("%s defines the JSON %s for the %s tool.", typeName, usage, tool.QualifiedName)
	info := &typeData{
		Key:           key,
		TypeName:      typeName,
		Doc:           doc,
		Def:           defLine,
		SchemaJSON:    schemaBytes,
		ExampleJSON:   exampleBytes,
		ExportedCodec: typeName + "Codec",
		GenericCodec:  lowerCamel(typeName) + "Codec",
		MarshalFunc:   "Marshal" + typeName,
		UnmarshalFunc: "Unmarshal" + typeName,
		ValidateFunc:  "Validate" + typeName,
		FullRef:       fullRef,
		NeedType:      defLine != "",
		IsToolType:    usage == usagePayload || usage == usageResult || usage == usageSidecar,
		NilError:      fmt.Sprintf("%s is nil", lowerCamel(typeName)),
		DecodeError:   fmt.Sprintf("decode %s", lowerCamel(typeName)),
		ValidateError: fmt.Sprintf("validate %s", lowerCamel(typeName)),
		EmptyError:    fmt.Sprintf("%s JSON is empty", lowerCamel(typeName)),
		Usage:         usage,
		TypeImports:   imports,
		GenerateCodec: true,
		Pointer:       ptr,
		MarshalArg:    "v",
		UnmarshalArg:  "v",
	}
	if usage == usagePayload && len(exampleBytes) > 0 {
		if eg, ok := exampleInputGoExpr(exampleBytes); ok {
			info.ExampleInputGo = eg
		}
	}
	// For tool payloads, untyped codecs should return pointers.
	// Record pointer intent via the flag; templates will render "*" where needed
	// using Goa NameScope-derived base references (no string surgery here).
	if usage == usagePayload {
		info.Pointer = true
	}
	// For tool results and sidecars, prefer pointers for object-shaped types.
	//
	// Tool result and sidecar values are typically produced by generated transforms
	// and service executors via address-taking composite literals (e.g. &T{...}).
	// Using pointer codecs makes the tool contract explicit and prevents accidental
	// fallback marshaling of unrelated service method types.
	if (usage == usageResult || usage == usageSidecar) && goaexpr.AsObject(baseAttr.Type) != nil {
		info.Pointer = true
	}
	// Populate JSON-body helpers (HTTP server body style) for payload, result,
	// and sidecar types unless delegating to service-level codecs. This keeps
	// tool JSON aligned with Goa HTTP behavior (including union Type/Value
	// encoding) and avoids encoding/json limitations with union carrier
	// interfaces.
	needJSONBody := usage == usagePayload || usage == usageResult || usage == usageSidecar
	if needJSONBody {
		// Do not generate standalone validation for the final payload type.
		if usage == usagePayload {
			info.ValidateFunc = ""
			info.Validation = ""
			info.ValidationSrc = nil
			// Accept empty JSON for payloads that are empty structs (no fields).
			if isEmptyStruct(att) {
				info.AcceptEmpty = true
			}
		}
		// Build JSON decode-body types uniformly, treating Empty as an empty object.
		jsonAttr := att
		if att.Type == goaexpr.Empty {
			jsonAttr = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
		}
		// 1) JSON decode-body type: materialize a single named user type for the
		// root body with inline nested objects (no separate nested user types).
		jsonRoot, jsonDefs := b.materializeJSONUserTypes(jsonAttr, typeName+"JSON", scope)
		// Ensure any union sum types referenced by the JSON helper graph are
		// emitted in unions.go. JSON helper types may include locally materialized
		// unions (e.g., type/value carriers) that do not exist in the target
		// package otherwise.
		b.collectUnionSumTypes(scope, &goaexpr.AttributeExpr{Type: jsonRoot})
		assertNoNilTypes(jsonRoot.Attribute(), tool, usage, "json-root")
		// Compute the final public name for the root JSON type so that
		// references in codecs match the emitted type name in types.go.
		// JSON helper types are emitted in the current package; use GoTypeName
		// so names are unqualified when no package is needed.
		jsonRootPublic := scope.GoTypeName(&goaexpr.AttributeExpr{Type: jsonRoot})
		info.JSONTypeName = jsonRootPublic
		info.JSONRef = jsonRootPublic

		// Emit the JSON root type as a standalone declaration.
		for _, jut := range jsonDefs {
			assertNoNilTypes(jut.Attribute(), tool, usage, "json-helper")
			// Ensure JSON helper fields carry struct tag metadata so GoTypeDef
			// can emit json tags that match the original field names (including
			// underscores). Without this, names like "device_aliases" will not
			// populate fields such as DeviceAliases correctly.
			if obj := goaexpr.AsObject(jut.Attribute().Type); obj != nil {
				for _, nat := range *obj {
					if nat == nil || nat.Attribute == nil {
						continue
					}
					if nat.Attribute.Meta == nil {
						nat.Attribute.Meta = make(map[string][]string)
					}
					// Only set when no tag is present so DSL authors can override it.
					if _, ok := nat.Attribute.Meta["struct:tag:json"]; !ok {
						nat.Attribute.Meta["struct:tag:json"] = []string{nat.Name}
					}
				}
			}

			// Use Goa scope to compute the final public name, which guarantees
			// consistency with references produced by GoTypeDef/GoTypeName.
			// Helper JSON types are local to the specs package; use GoTypeName
			// to keep names unqualified.
			jattr := &goaexpr.AttributeExpr{Type: jut}
			gname := scope.GoTypeName(jattr)
			def := gname + " = " + scope.GoTypeDef(jut.Attribute(), true, false)
			ref := scope.GoTypeRef(jattr)
			// Generate standalone validator for this JSON helper user type so
			// root validators that call Validate<Helper> compile.
			httpctx := codegen.NewAttributeContext(true, false, false, "", scope)
			vcode := validationCodeWithContext(jut.Attribute(), jut, httpctx, true, false, false, "body", tool, usage, "json-helper:"+gname)
			td := &typeData{
				Key:          "json:" + gname,
				TypeName:     gname,
				Doc:          gname + " is a helper type for JSON decode-body.",
				Def:          def,
				FullRef:      ref,
				NeedType:     true,
				TypeImports:  gatherAttributeImports(b.genpkg, jut.Attribute()),
				ValidateFunc: "Validate" + gname,
				Validation:   vcode,
			}
			td.ValidationSrc = strings.Split(vcode, "\n")
			if _, exists := b.types[td.Key]; !exists {
				b.types[td.Key] = td
			}
		}

		// 2) Validation against JSON body using HTTP server-like AttributeContext
		httpctx := codegen.NewAttributeContext(true, false, false, "", scope)
		jv := validationCodeWithContext(jsonRoot.Attribute(), jsonRoot, httpctx, true, false, false, "raw", tool, usage, "json-root")
		if jv != "" {
			info.JSONValidation = jv
			info.JSONValidationSrc = strings.Split(jv, "\n")
		}

		// 3) Transform raw(JSON) -> final type.
		// For empty payloads/results, emit a direct initializer to the local alias
		// to avoid Goa's transform generating '&Empty{}'.
		if att.Type == goaexpr.Empty {
			info.TransformBody = "v := &" + typeName + "{}"
		} else {
			srcAttr := &goaexpr.AttributeExpr{Type: jsonRoot}
			tgtAttr := &goaexpr.AttributeExpr{Type: toolUT}
			// Use the same NameScope used for emitting JSON helper types so that
			// GoTransform references match the generated type names.
			srcCtx := codegen.NewAttributeContext(true, false, false, "", scope)
			tgtCtx := codegen.NewAttributeContext(false, false, true, "", scope)
			typeRef := tgtCtx.Scope.Ref(tgtAttr, tgtCtx.Pkg(tgtAttr))
			if strings.HasPrefix(typeRef, "*") {
				body, helpers, err := codegen.GoTransform(srcAttr, tgtAttr, "raw", "v", srcCtx, tgtCtx, string(usage), true)
				if err == nil && body != "" {
					info.TransformBody = body
					if len(helpers) > 0 {
						info.TransformHelpers = fixTransformHelperSignatures(helpers)
					}
				}
			} else {
				body, helpers, err := codegen.GoTransform(srcAttr, tgtAttr, "raw", "res", srcCtx, tgtCtx, string(usage), true)
				if err == nil && body != "" {
					body += "\nv := &res"
					info.TransformBody = body
					if len(helpers) > 0 {
						info.TransformHelpers = fixTransformHelperSignatures(helpers)
					}
				}
			}
		}

		// Keep field descriptions for validation error enrichment
		if fdesc := buildFieldDescriptions(tt); len(fdesc) > 0 {
			info.FieldDescs = fdesc
		}
	}
	// For bounded tool results, mark the type as implementing agent.BoundedResult
	// so templates can emit a simple ResultBounds method that exposes the
	// canonical Bounds contract. Bounded results are decoded as pointers so the
	// runtime can reliably detect agent.BoundedResult via type assertions
	// (deriveBounds) when enforcing bounded-view contracts.
	if usage == usageResult && tool.BoundedResult {
		info.ImplementsBounds = true
		// Force pointer semantics for bounded results so codecs return
		// *<ResultType>. This ensures decoded values implement the
		// agent.BoundedResult interface (which has a pointer receiver) and
		// allows the runtime to derive Bounds metadata from the typed result.
		info.Pointer = true
		const agentPath = "goa.design/goa-ai/runtime/agent"
		hasAgentImport := false
		for _, im := range info.TypeImports {
			if im != nil && im.Path == agentPath {
				hasAgentImport = true
				break
			}
		}
		if !hasAgentImport {
			info.TypeImports = append(info.TypeImports, &codegen.ImportSpec{
				Name: "agent",
				Path: agentPath,
			})
		}
	}
	b.types[key] = info
	// Also index by the public type name so auxiliary passes (e.g.,
	// validator collection) can detect that a concrete alias already
	// exists and avoid emitting duplicate helpers for the same name.
	nameKey := "name:" + typeName
	if _, exists := b.types[nameKey]; !exists {
		b.types[nameKey] = info
	}
	// Also ensure any nested service-local user types are materialized locally so
	// unqualified references inside composite shapes compile.
	b.ensureNestedLocalTypes(scope, tt)
	// Collect validators for all unique user types referenced within payloads
	// so nested validations do not rely on external packages to provide local
	// Validate<Name> functions.
	if usage == usagePayload {
		b.collectUserTypeValidators(scope, tool, usage, tt)
	}
	return info, nil
}

func schemaAttributeWithArtifactsToggle(tool *ToolData, att *goaexpr.AttributeExpr) (*goaexpr.AttributeExpr, error) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att, nil
	}
	if ut, ok := att.Type.(goaexpr.UserType); ok {
		base := ut.Attribute()
		mod, err := addArtifactsToggleToObjectAttribute(tool, base)
		if err != nil {
			return nil, err
		}
		dup := *att
		dup.Type = ut.Dup(mod)
		return &dup, nil
	}
	return addArtifactsToggleToObjectAttribute(tool, att)
}

func addArtifactsToggleToObjectAttribute(tool *ToolData, att *goaexpr.AttributeExpr) (*goaexpr.AttributeExpr, error) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att, nil
	}
	obj := goaexpr.AsObject(att.Type)
	if obj == nil {
		return nil, fmt.Errorf(
			"tool %q declares artifacts but payload is not an object; artifact toggles require an object payload",
			tool.QualifiedName,
		)
	}
	for _, na := range *obj {
		if na != nil && na.Name == "artifacts" {
			return att, nil
		}
	}

	dup := *att
	dupObj := make(goaexpr.Object, 0, len(*obj)+1)
	dupObj = append(dupObj, *obj...)
	dupObj = append(dupObj, &goaexpr.NamedAttributeExpr{
		Name: "artifacts",
		Attribute: &goaexpr.AttributeExpr{
			Type:        goaexpr.String,
			Description: "Controls whether UI artifacts are produced for this tool call. Valid values: \"auto\", \"on\", \"off\".",
			Validation: &goaexpr.ValidationExpr{
				Values: []any{
					string(runtimetools.ArtifactsModeAuto),
					string(runtimetools.ArtifactsModeOn),
					string(runtimetools.ArtifactsModeOff),
				},
			},
		},
	})
	dup.Type = &dupObj
	return &dup, nil
}

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
			b.WriteString(fmt.Sprintf("%#v", k))
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

// isEmptyStruct reports whether the provided attribute ultimately resolves to
// an object with no fields (empty struct). It follows user type aliases to
// inspect the underlying attribute graph.
