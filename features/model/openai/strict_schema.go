package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

// strict_schema.go owns the OpenAI strict-mode schema projection and its
// inverse. The adapter always requests function tools and structured outputs
// with strict:true, and OpenAI only accepts a constrained JSON Schema subset
// in that mode: every object must set additionalProperties:false and list all
// of its properties as required, with optionality expressed as a null type
// union. The canonical generated schema stays provider-neutral and remains
// the source of truth for local decoding; this file either produces an
// equivalent strict schema or rejects the contract explicitly when OpenAI
// cannot represent it (open objects and map-style additionalProperties).
//
// The projection introduces exactly one model-visible artifact: canonically
// optional members become nullable, so strict decoding emits explicit null
// for members the model wants to omit. canonicalizeStrictPayload is the exact
// inverse: it removes null members wherever the canonical schema does not
// accept null. Those locations are precisely the ones the projection
// rewrote, so payloads handed to the runtime keep the canonical encoding of
// absence while members whose canonical contracts accept null pass through
// untouched.

const (
	strictSchemaTypeObject = "object"
	strictSchemaTypeString = "string"
	strictSchemaTypeNull   = "null"
)

var (
	// strictUnsupportedKeywords are annotation keywords OpenAI strict mode does
	// not accept or that all-members-required semantics make meaningless. The
	// canonical schema keeps them for local decoding; the provider copy drops
	// them.
	strictUnsupportedKeywords = []string{"$schema", "example", "examples", "default"}

	// strictSupportedStringFormats are the format values OpenAI strict mode
	// accepts on string schemas. Goa also stamps numeric formats such as int64
	// that strict mode rejects, so format survives only on string schemas with
	// a supported value.
	strictSupportedStringFormats = map[string]struct{}{
		"date-time": {},
		"time":      {},
		"date":      {},
		"duration":  {},
		"email":     {},
		"hostname":  {},
		"ipv4":      {},
		"ipv6":      {},
		"uuid":      {},
	}

	// strictChildSchemaListKeywords name children that are lists of schemas.
	strictChildSchemaListKeywords = []string{"anyOf", "oneOf", "allOf"}

	// strictChildSchemaMapKeywords name children whose immediate map keys are
	// user-chosen names (property or definition names), never schema keywords.
	// Keyword handling must not apply at that level: a property legitimately
	// named "default" is data, not a keyword.
	strictChildSchemaMapKeywords = []string{"properties", "$defs", "definitions"}
)

// projectStrictSchema rewrites one canonical JSON Schema document into the
// subset OpenAI strict mode accepts and returns it in the decoded form the SDK
// request types expect. Empty canonical schemas project to the closed empty
// object. Callers wrap returned errors with the owning tool or
// structured-output name.
func projectStrictSchema(schema rawjson.Message) (map[string]any, error) {
	data := bytes.TrimSpace(schema)
	if len(data) == 0 {
		return map[string]any{"type": strictSchemaTypeObject, "additionalProperties": false}, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid JSON schema: %w", err)
	}
	if !includesSchemaType(doc, strictSchemaTypeObject) {
		return nil, fmt.Errorf("schema root must declare type %q; OpenAI strict mode only accepts object payloads", strictSchemaTypeObject)
	}
	if err := projectStrictNode(doc, "$"); err != nil {
		return nil, err
	}
	return doc, nil
}

// canonicalizeStrictPayload restores the canonical encoding of absence in a
// strict-mode payload: the projection makes optional members nullable, so the
// model emits explicit null to omit them. Null members are removed exactly
// where the canonical schema does not accept null; by construction those are
// the members the projection rewrote. schema is the canonical (unprojected)
// schema. Payloads without projection artifacts are returned unchanged.
func canonicalizeStrictPayload(schema, payload rawjson.Message) (rawjson.Message, error) {
	schemaData := bytes.TrimSpace(schema)
	payloadData := bytes.TrimSpace(payload)
	if len(schemaData) == 0 || len(payloadData) == 0 {
		return payload, nil
	}
	var root map[string]any
	if err := json.Unmarshal(schemaData, &root); err != nil {
		return nil, fmt.Errorf("invalid canonical schema: %w", err)
	}
	var doc any
	if err := json.Unmarshal(payloadData, &doc); err != nil {
		return nil, fmt.Errorf("invalid payload JSON: %w", err)
	}
	if !canonicalizeStrictValue(resolveStrictSchemas(root, root, nil), doc, root) {
		return payload, nil
	}
	normalized, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal canonicalized payload: %w", err)
	}
	return rawjson.Message(normalized), nil
}

// projectStrictNode rewrites one schema node in place and recurses through the
// keyword positions that hold child schemas. Recursion is keyword-driven so
// instance data such as enum values is never mistaken for schema. path names
// the node in rejection errors.
func projectStrictNode(node map[string]any, path string) error {
	for _, keyword := range strictUnsupportedKeywords {
		delete(node, keyword)
	}
	projectStrictFormat(node)
	if includesSchemaType(node, strictSchemaTypeObject) {
		if err := projectStrictObject(node, path); err != nil {
			return err
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		if err := projectStrictNode(items, path+".items"); err != nil {
			return err
		}
	}
	for _, keyword := range strictChildSchemaListKeywords {
		branches, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		for i, branch := range branches {
			branchMap, ok := branch.(map[string]any)
			if !ok {
				continue
			}
			if err := projectStrictNode(branchMap, fmt.Sprintf("%s.%s[%d]", path, keyword, i)); err != nil {
				return err
			}
		}
	}
	for _, keyword := range strictChildSchemaMapKeywords {
		children, ok := node[keyword].(map[string]any)
		if !ok {
			continue
		}
		for name, child := range children {
			childMap, ok := child.(map[string]any)
			if !ok {
				continue
			}
			if err := projectStrictNode(childMap, path+"."+keyword+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

// projectStrictObject enforces the strict closed-object contract: objects
// declare additionalProperties:false and every property is required, with
// canonically optional properties made nullable so the model can still omit
// them by emitting null.
func projectStrictObject(node map[string]any, path string) error {
	switch additional := node["additionalProperties"].(type) {
	case nil:
		node["additionalProperties"] = false
	case bool:
		if additional {
			return fmt.Errorf("schema at %s declares an open object; OpenAI strict mode requires closed objects", path)
		}
	default:
		return fmt.Errorf("schema at %s declares a map-style object; OpenAI strict mode cannot represent open maps", path)
	}
	properties, ok := node["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		delete(node, "required")
		return nil
	}
	required := make(map[string]struct{})
	if names, ok := node["required"].([]any); ok {
		for _, name := range names {
			if s, ok := name.(string); ok {
				required[s] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, isRequired := required[name]; isRequired {
			continue
		}
		if property, ok := properties[name].(map[string]any); ok {
			makeStrictNullable(property)
		}
	}
	allRequired := make([]any, len(names))
	for i, name := range names {
		allRequired[i] = name
	}
	node["required"] = allRequired
	return nil
}

// projectStrictFormat keeps only the string formats OpenAI strict mode
// accepts and drops format from every non-string schema.
func projectStrictFormat(node map[string]any) {
	raw, present := node["format"]
	if !present {
		return
	}
	format, ok := raw.(string)
	if !ok || !includesSchemaType(node, strictSchemaTypeString) {
		delete(node, "format")
		return
	}
	if _, supported := strictSupportedStringFormats[format]; !supported {
		delete(node, "format")
	}
}

// makeStrictNullable rewrites one property schema so null becomes an accepted
// value: strict mode requires every member to be present, so null is how the
// model omits a canonically optional member.
func makeStrictNullable(property map[string]any) {
	if enum, ok := property["enum"].([]any); ok && !containsJSONNull(enum) {
		property["enum"] = append(enum, nil)
	}
	switch declared := property["type"].(type) {
	case string:
		if declared != strictSchemaTypeNull {
			property["type"] = []any{declared, strictSchemaTypeNull}
		}
		return
	case []any:
		if !containsSchemaTypeName(declared, strictSchemaTypeNull) {
			property["type"] = append(declared, strictSchemaTypeNull)
		}
		return
	}
	if branches, ok := property["anyOf"].([]any); ok {
		if !strictBranchesAcceptNull(branches) {
			property["anyOf"] = append(branches, map[string]any{"type": strictSchemaTypeNull})
		}
		return
	}
	if ref, ok := property["$ref"]; ok {
		delete(property, "$ref")
		property["anyOf"] = []any{
			map[string]any{"$ref": ref},
			map[string]any{"type": strictSchemaTypeNull},
		}
	}
	// No type, union, or reference constrains the property, so the schema
	// already accepts null.
}

// canonicalizeStrictValue walks one payload value alongside the canonical
// schemas that can govern it, removing null members the canonical contract
// does not accept. It mutates the payload in place and reports whether it
// changed anything.
func canonicalizeStrictValue(candidates []map[string]any, value any, root map[string]any) bool {
	changed := false
	switch actual := value.(type) {
	case map[string]any:
		for name, member := range actual {
			memberCandidates := memberStrictSchemas(candidates, name, root)
			if member == nil {
				if len(memberCandidates) > 0 && !strictSchemasAcceptNull(memberCandidates) {
					delete(actual, name)
					changed = true
				}
				continue
			}
			if canonicalizeStrictValue(memberCandidates, member, root) {
				changed = true
			}
		}
	case []any:
		itemCandidates := itemStrictSchemas(candidates, root)
		for _, element := range actual {
			if canonicalizeStrictValue(itemCandidates, element, root) {
				changed = true
			}
		}
	}
	return changed
}

// resolveStrictSchemas expands one schema node into the concrete schemas that
// can govern an instance location: local $refs are followed and branch
// keywords contribute their branches independently. Unresolvable references
// contribute nothing, which keeps unknown contracts untouched.
func resolveStrictSchemas(node map[string]any, root map[string]any, seen map[string]struct{}) []map[string]any {
	if node == nil {
		return nil
	}
	if ref, ok := node["$ref"].(string); ok {
		if _, cycling := seen[ref]; cycling {
			return nil
		}
		target := resolveLocalSchemaRef(root, ref)
		if target == nil {
			return nil
		}
		next := make(map[string]struct{}, len(seen)+1)
		for key := range seen {
			next[key] = struct{}{}
		}
		next[ref] = struct{}{}
		return resolveStrictSchemas(target, root, next)
	}
	var out []map[string]any
	branched := false
	for _, keyword := range strictChildSchemaListKeywords {
		branches, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		branched = true
		for _, branch := range branches {
			if branchMap, ok := branch.(map[string]any); ok {
				out = append(out, resolveStrictSchemas(branchMap, root, seen)...)
			}
		}
	}
	if hasDirectStrictConstraints(node) || !branched {
		out = append(out, node)
	}
	return out
}

// memberStrictSchemas returns the resolved schemas governing one object member
// across all candidate object schemas.
func memberStrictSchemas(candidates []map[string]any, name string, root map[string]any) []map[string]any {
	var out []map[string]any
	for _, candidate := range candidates {
		properties, ok := candidate["properties"].(map[string]any)
		if !ok {
			continue
		}
		member, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		out = append(out, resolveStrictSchemas(member, root, nil)...)
	}
	return out
}

// itemStrictSchemas returns the resolved schemas governing array elements
// across all candidate array schemas.
func itemStrictSchemas(candidates []map[string]any, root map[string]any) []map[string]any {
	var out []map[string]any
	for _, candidate := range candidates {
		if items, ok := candidate["items"].(map[string]any); ok {
			out = append(out, resolveStrictSchemas(items, root, nil)...)
		}
	}
	return out
}

// strictSchemasAcceptNull reports whether any candidate schema accepts null.
func strictSchemasAcceptNull(candidates []map[string]any) bool {
	return slices.ContainsFunc(candidates, strictSchemaAcceptsNull)
}

// strictSchemaAcceptsNull reports whether one resolved schema accepts null.
// Enums decide when present; otherwise a declared type must include "null".
// Schemas that declare neither accept every value, including null.
func strictSchemaAcceptsNull(schema map[string]any) bool {
	if enum, ok := schema["enum"].([]any); ok {
		return containsJSONNull(enum)
	}
	switch declared := schema["type"].(type) {
	case string:
		return declared == strictSchemaTypeNull
	case []any:
		return containsSchemaTypeName(declared, strictSchemaTypeNull)
	}
	return true
}

// hasDirectStrictConstraints reports whether the schema node constrains
// instances directly rather than delegating entirely to branch schemas.
func hasDirectStrictConstraints(schema map[string]any) bool {
	for _, keyword := range []string{"type", "enum", "properties", "items"} {
		if _, ok := schema[keyword]; ok {
			return true
		}
	}
	return false
}

// strictBranchesAcceptNull reports whether an anyOf branch list already
// contains a null-accepting branch.
func strictBranchesAcceptNull(branches []any) bool {
	return slices.ContainsFunc(branches, func(branch any) bool {
		branchMap, ok := branch.(map[string]any)
		return ok && includesSchemaType(branchMap, strictSchemaTypeNull)
	})
}

// resolveLocalSchemaRef walks a document-local JSON pointer reference such as
// "#/$defs/Name" through the schema document. Unknown or external references
// return nil.
func resolveLocalSchemaRef(root map[string]any, ref string) map[string]any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	node := any(root)
	for segment := range strings.SplitSeq(strings.TrimPrefix(ref, "#/"), "/") {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		current, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = current[segment]
	}
	target, _ := node.(map[string]any)
	return target
}

// includesSchemaType reports whether a schema node declares the requested
// type, including union forms such as ["object","null"].
func includesSchemaType(node map[string]any, want string) bool {
	switch declared := node["type"].(type) {
	case string:
		return declared == want
	case []any:
		return containsSchemaTypeName(declared, want)
	}
	return false
}

// containsSchemaTypeName reports whether a type union names the given type.
func containsSchemaTypeName(types []any, want string) bool {
	return slices.ContainsFunc(types, func(entry any) bool {
		name, ok := entry.(string)
		return ok && name == want
	})
}

// containsJSONNull reports whether an enum value list contains JSON null.
func containsJSONNull(values []any) bool {
	return slices.Contains(values, nil)
}
