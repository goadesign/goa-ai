// Package codegen compiles Inject() field resolution at generation time.
//
// Inject() marks a tool payload field as server-populated and hidden from the
// model (see codegen/agent/prepare.go's flattenAndHide). This file resolves
// each injected name to its concrete source once, at generation time, so the
// generated per-toolset inject.go never has to interpret names at runtime:
//
//   - Names that Goify to a runtime.ToolCallMeta field (SessionID, RunID,
//     TurnID, ToolCallID, ParentToolCallID) compile to a direct meta read.
//   - Every other name compiles to a run-label lookup, with a compiled
//     missing-label error and the field's own generated validation (via
//     goa's codegen.AttributeValidationCode, the same helper Goa itself uses
//     to validate individual HTTP path/query parameters) applied to the
//     label value.
//
// expr/agent/tool.go's Validate already guarantees, before this file ever
// runs, that every injected name exists on the effective payload, is
// required, and is a String; this file trusts those invariants.
package codegen

import (
	"sort"
	"strings"

	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// toolInjectFileData is the template data for a toolset's generated
	// inject.go, which defines one Inject<Tool> function per tool that
	// declares at least one Inject() field.
	toolInjectFileData struct {
		// Tools lists every tool in the toolset; the template skips tools
		// with no Injected entries.
		Tools []*ToolData
	}

	// InjectedFieldData is the compiled source resolution for one Inject()
	// field, ready for the tool_inject.go.tpl template.
	InjectedFieldData struct {
		// Name is the design-time Inject() field name.
		Name string
		// GoFieldName is the Go struct field name on the tool's generated
		// payload type (e.g., "SessionID" for design name "session_id").
		GoFieldName string
		// IsMetaBacked is true when the field is populated directly from
		// runtime.ToolCallMeta rather than from a run label.
		IsMetaBacked bool
		// MetaField is the runtime.ToolCallMeta field name to copy from. Set
		// only when IsMetaBacked is true.
		MetaField string
		// LabelKey is the run label key to look up. Set only when
		// IsMetaBacked is false; always equal to Name.
		LabelKey string
		// ValidationCode is the Go source (assuming a local variable "v" of
		// type string and a pre-existing "err error" variable) that runs the
		// field's declared validations (pattern, length, enum, format).
		// Empty when the field declares no extra validation beyond being
		// required, which Inject already enforces via the label lookup.
		ValidationCode string
	}
)

// metaFieldByGoName is the fixed set of ToolCallMeta fields Inject() can
// compile a direct read from. Keys are the Go field names (post-Goify).
var metaFieldByGoName = map[string]struct{}{
	"RunID":            {},
	"SessionID":        {},
	"TurnID":           {},
	"ToolCallID":       {},
	"ParentToolCallID": {},
}

// buildInjectedFields resolves tool.InjectedFields against the effective
// tool Args (post codegen Prepare: explicit Args when declared, otherwise the
// bound method payload copied in for method-backed tools) into compiled
// InjectedFieldData entries.
//
// payload must be part of the attribute sets expr/agent/tool.go's Validate
// already checked the injected names against (see injectTargets there):
// every name is guaranteed to exist, be required, and be a String.
func buildInjectedFields(payload *goaexpr.AttributeExpr, names []string) []*InjectedFieldData {
	if len(names) == 0 {
		return nil
	}
	obj := effectiveObject(payload)
	out := make([]*InjectedFieldData, 0, len(names))
	for _, name := range names {
		field := obj.Attribute(name)
		data := &InjectedFieldData{
			Name:        name,
			GoFieldName: codegen.Goify(name, true),
		}
		if metaField, ok := injectedFieldSource(name); ok {
			data.IsMetaBacked = true
			data.MetaField = metaField
		} else {
			data.LabelKey = name
			data.ValidationCode = fieldValidationCode(field, name)
		}
		out = append(out, data)
	}
	return out
}

// requiredLabels returns the sorted, deduplicated set of label keys that
// label-backed Inject() fields require across the toolset's tools. It is
// exposed on the generated specs package as RequiredLabels so the runtime
// can validate WithLabels(...) coverage at run start (Task 2).
func requiredLabels(tools []*ToolData) []string {
	seen := make(map[string]struct{})
	for _, t := range tools {
		if t == nil {
			continue
		}
		for _, inj := range t.Injected {
			if inj.IsMetaBacked {
				continue
			}
			seen[inj.LabelKey] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// toolsNeedInject reports whether any tool in tools declares at least one
// Inject() field, i.e., whether the toolset needs a generated inject.go.
func toolsNeedInject(tools []*ToolData) bool {
	for _, t := range tools {
		if t != nil && len(t.Injected) > 0 {
			return true
		}
	}
	return false
}

// methodToolsNeedInject reports whether any METHOD-BACKED tool declares at
// least one Inject() field. The generated registry provider.go only emits
// dispatch cases for method-backed tools, so its runtime.ToolCallMeta
// construction (and the runtime package import) must be gated on this
// narrower predicate: gating on toolsNeedInject would emit a
// declared-and-unused meta variable -- a generated-code compile failure --
// for toolsets mixing a non-injecting bound tool with an injecting unbound
// tool.
func methodToolsNeedInject(tools []*ToolData) bool {
	for _, t := range tools {
		if t != nil && t.IsMethodBacked && len(t.Injected) > 0 {
			return true
		}
	}
	return false
}

// toolInjectImports returns the imports required by the generated inject.go.
// "goa" and "unicode/utf8" are only pulled in when at least one field's
// compiled validation code references them, mirroring
// toolSpecsData.needsGoaImport/needsUnicodeImport for http/validate.go.
func toolInjectImports(tools []*ToolData) []*codegen.ImportSpec {
	imports := []*codegen.ImportSpec{
		codegen.SimpleImport("fmt"),
		{Path: "goa.design/goa-ai/runtime/agent/runtime"},
	}
	var needsGoa, needsUnicode bool
	for _, t := range tools {
		if t == nil {
			continue
		}
		for _, inj := range t.Injected {
			if inj.ValidationCode == "" {
				continue
			}
			needsGoa = true
			if strings.Contains(inj.ValidationCode, "utf8.") {
				needsUnicode = true
			}
		}
	}
	if needsGoa {
		imports = append(imports, codegen.GoaImport(""))
	}
	if needsUnicode {
		imports = append(imports, codegen.SimpleImport("unicode/utf8"))
	}
	return imports
}

// injectedFieldSource classifies name's compiled source: it is meta-backed
// when Goifying it matches a runtime.ToolCallMeta field, regardless of
// whether the design used snake_case ("session_id") or lowerCamel
// ("sessionId") -- both Goify to "SessionID". Every other name is
// label-backed.
func injectedFieldSource(name string) (metaField string, isMeta bool) {
	gn := codegen.Goify(name, true)
	if _, ok := metaFieldByGoName[gn]; ok {
		return gn, true
	}
	return "", false
}

// effectiveObject unwraps a payload attribute (dereferencing a user type
// wrapper, if any) down to the underlying Object so individual fields can be
// looked up by name. Callers only reach here after expr/agent/tool.go's
// Validate has already confirmed the payload is an object.
func effectiveObject(payload *goaexpr.AttributeExpr) *goaexpr.Object {
	att := payload
	if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
		att = ut.Attribute()
	}
	obj, _ := att.Type.(*goaexpr.Object)
	return obj
}

// fieldValidationCode generates the Go source that runs field's declared
// validations (pattern, length, enum, format) against a local string
// variable named "v", reusing goa's own per-attribute validation codegen
// (the same helper that generates HTTP path/query parameter validation) so
// Inject never duplicates validation rules by hand. Returns "" when the
// field declares no validation beyond being required (already enforced by
// the compiled missing-label check).
func fieldValidationCode(field *goaexpr.AttributeExpr, attName string) string {
	if field == nil {
		return ""
	}
	attCtx := codegen.NewAttributeContext(false, false, false, "", codegen.NewNameScope())
	code := codegen.AttributeValidationCode(field, nil, attCtx, true, false, "v", attName)
	return strings.TrimSpace(code)
}

