// Package mcp checks skill discovery during DSL evaluation. Complete manifests
// use native typed unions; file reads retain the service's existing URI owner.
// These checks reject contracts that generated protocol methods cannot preserve.
package mcp

import (
	"slices"

	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// validateSkills requires both discovery owners and the existing file reader.
// Each authored result is checked before generation can advertise the extension.
func (m *MCPExpr) validateSkills(verr *eval.ValidationErrors) {
	if m.SkillCatalog == nil && m.SkillLookup == nil {
		return
	}
	if m.SkillCatalog == nil || m.SkillLookup == nil {
		verr.Add(m, "MCP Skills requires both SkillCatalog and SkillLookup")
	}
	if m.ResourceReader == nil {
		verr.Add(m, "MCP Skills requires ResourceReader for skill files")
	}
	if method := m.SkillCatalog; method != nil {
		validateCatalogMethod(verr, method, "skills")
		result := mcpinput.Resolved(method.Result)
		if collection := result.Find("skills"); collection != nil {
			if array := expr.AsArray(collection.Type); array != nil {
				validateSkillEntry(verr, method, array.ElemType)
			}
		}
	}
	if method := m.SkillLookup; method != nil {
		validateSkillLookup(verr, method)
	}
}

// validateSkillLookup checks the direct URI request and complete returned entry.
// Native credentials and mapped route values remain outside protocol parameters.
func validateSkillLookup(verr *eval.ValidationErrors, method *expr.MethodExpr) {
	if method.IsStreaming() {
		verr.Add(method, "skill lookup method must be unary")
	}
	for _, key := range []string{mcpinput.ExchangeMetaKey, mcpinput.TaskExchangeMetaKey} {
		if _, declared := method.Meta[key]; declared {
			verr.Add(method, "skill lookup must return a completed entry")
		}
	}
	arguments, err := mcpinput.Arguments(method)
	if err != nil {
		verr.Add(method, "%s", err.Error())
		return
	}
	payload := expr.AsObject(arguments.Type)
	if payload == nil || len(*payload) != 1 || payload.Attribute("uri") == nil || !isPrimitive(payload.Attribute("uri").Type, expr.String) || !arguments.IsRequired("uri") {
		verr.Add(method, "skill lookup payload must contain only a required uri string apart from native HTTP inputs")
	}
	result := mcpinput.Resolved(method.Result)
	object := expr.AsObject(result.Type)
	if object == nil || len(*object) != 1 || object.Attribute("skill") == nil || !result.IsRequired("skill") {
		verr.Add(method, "skill lookup result must contain only a required skill object")
		return
	}
	validateSkillEntry(verr, method, object.Attribute("skill"))
}

// validateSkillEntry checks the exact entry fields and file manifest shape.
// Frontmatter remains open JSON because the extension preserves future fields;
// hosts verify its contents against the YAML read from SKILL.md before loading it.
func validateSkillEntry(verr *eval.ValidationErrors, method *expr.MethodExpr, entry *expr.AttributeExpr) {
	object := expr.AsObject(entry.Type)
	if object == nil || len(*object) != 3 {
		verr.Add(method, "skill entry must contain only required uri, frontmatter and resources")
		return
	}
	for _, field := range []string{"uri", "frontmatter", "resources"} {
		if object.Attribute(field) == nil || !entry.IsRequired(field) {
			verr.Add(method, "skill entry must declare required %s", field)
			return
		}
	}
	uri := object.Attribute("uri")
	if !isPrimitive(uri.Type, expr.String) || expr.EffectiveValidation(uri) == nil || expr.EffectiveValidation(uri).Format != expr.FormatURI {
		verr.Add(method, "skill uri must use String with FormatURI")
	}
	frontmatter := object.Attribute("frontmatter")
	if !isPrimitive(frontmatter.Type, expr.Any) || !slices.Equal(frontmatter.Meta["struct:field:type"], []string{"json.RawMessage", "encoding/json"}) {
		verr.Add(method, "skill frontmatter must use json.RawMessage to preserve all authored fields")
	}
	resources := expr.AsUnion(object.Attribute("resources").Type)
	if resources == nil || !resources.Untagged || len(resources.Values) != 2 {
		verr.Add(method, "skill resources must use an untagged OneOf of a file array and the string dynamic")
		return
	}
	for _, branch := range resources.Values {
		if array := expr.AsArray(branch.Attribute.Type); array != nil {
			if branch.Name != "manifest" {
				verr.Add(method, "skill file array branch must be named manifest")
			}
			if !array.NonNullableElems {
				verr.Add(method, "skill manifest must use ArrayOfRequired")
			}
			validateSkillFile(verr, method, array.ElemType)
			continue
		}
		validation := expr.EffectiveValidation(branch.Attribute)
		if branch.Name != "dynamic" || !isPrimitive(branch.Attribute.Type, expr.String) || validation == nil || !slices.Equal(validation.Values, []any{"dynamic"}) {
			verr.Add(method, "skill resources string branch must declare Enum(dynamic)")
		}
	}
}

// validateSkillFile keeps addresses, digests and byte lengths in typed fields.
// Complete membership and agreement with file bytes are checked at the host.
func validateSkillFile(verr *eval.ValidationErrors, method *expr.MethodExpr, file *expr.AttributeExpr) {
	object := expr.AsObject(file.Type)
	if object == nil || len(*object) != 3 {
		verr.Add(method, "skill file must contain only required uri, digest and size")
		return
	}
	for _, field := range []string{"uri", "digest", "size"} {
		if object.Attribute(field) == nil || !file.IsRequired(field) {
			verr.Add(method, "skill file must declare required %s", field)
			return
		}
	}
	uri := object.Attribute("uri")
	if !isPrimitive(uri.Type, expr.String) || expr.EffectiveValidation(uri) == nil || expr.EffectiveValidation(uri).Format != expr.FormatURI {
		verr.Add(method, "skill file uri must use String with FormatURI")
	}
	digest := object.Attribute("digest")
	if !isPrimitive(digest.Type, expr.String) || expr.EffectiveValidation(digest) == nil || expr.EffectiveValidation(digest).Pattern != `^sha256:[0-9a-f]{64}$` {
		verr.Add(method, "skill file digest must declare Pattern(^sha256:[0-9a-f]{64}$)")
	}
	size := object.Attribute("size")
	if !isPrimitive(size.Type, expr.Int64) || expr.EffectiveValidation(size) == nil || expr.EffectiveValidation(size).Minimum == nil || *expr.EffectiveValidation(size).Minimum < 0 {
		verr.Add(method, "skill file size must use Int64 with Minimum(0) in bytes")
	}
}
