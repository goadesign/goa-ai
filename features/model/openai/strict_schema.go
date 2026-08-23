package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

// strict_schema.go owns the OpenAI strict-mode schema projection. The adapter
// always requests function tools and structured outputs
// with strict:true, and OpenAI only accepts a constrained JSON Schema subset
// in that mode: every object must set additionalProperties:false and list all
// of its properties as required, with optionality expressed as a null type
// union, and unions expressed only as anyOf. The canonical generated schema
// stays provider-neutral and remains the source of truth for local decoding;
// this file either produces a generation-equivalent strict schema — one that
// accepts every instance the canonical schema accepts, folding oneOf into
// anyOf and dropping constraints strict mode rejects — or rejects the
// contract explicitly when OpenAI cannot represent it (open objects and
// map-style additionalProperties).
//
// The projection can make canonically optional members nullable because strict
// mode requires every object member. Returned bytes are never rewritten; the
// generated codec remains authoritative and rejects explicit null where the
// canonical contract does not accept it.

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
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON schema")
	}
	var doc map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
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

// projectStrictNode rewrites one schema node in place and recurses through the
// keyword positions that hold child schemas. Recursion is keyword-driven so
// instance data such as enum values is never mistaken for schema. path names
// the node in rejection errors.
func projectStrictNode(node map[string]any, path string) error {
	for _, keyword := range strictUnsupportedKeywords {
		delete(node, keyword)
	}
	projectStrictFormat(node)
	projectStrictUnion(node)
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

// projectStrictUnion folds oneOf branches into anyOf: strict mode only
// accepts anyOf, and for generation guidance anyOf accepts a superset of the
// instances oneOf accepts. The canonical schema keeps exclusive oneOf
// semantics for local validation.
func projectStrictUnion(node map[string]any) {
	branches, ok := node["oneOf"].([]any)
	if !ok {
		return
	}
	delete(node, "oneOf")
	if existing, ok := node["anyOf"].([]any); ok {
		node["anyOf"] = append(existing, branches...)
		return
	}
	node["anyOf"] = branches
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
// model omits a canonically optional member. Unions fold into anyOf first so
// the null branch lands in the strict-representable form; the later visit of
// this property during recursion finds nothing left to fold.
func makeStrictNullable(property map[string]any) {
	projectStrictUnion(property)
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

// strictBranchesAcceptNull reports whether an anyOf branch list already
// contains a null-accepting branch.
func strictBranchesAcceptNull(branches []any) bool {
	return slices.ContainsFunc(branches, func(branch any) bool {
		branchMap, ok := branch.(map[string]any)
		return ok && includesSchemaType(branchMap, strictSchemaTypeNull)
	})
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
