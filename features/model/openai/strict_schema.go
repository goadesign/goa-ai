package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

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
// accepts every instance the canonical schema accepts, folding provably
// exclusive oneOf branches into anyOf and dropping constraints strict mode
// rejects — or rejects the contract explicitly when OpenAI cannot represent it
// (overlapping oneOf branches, open objects, and map-style
// additionalProperties).
//
// The projection can make canonically optional members nullable because strict
// mode requires every object member. The same compiled projection removes only
// those transport-only null members before generated canonical validation.

const (
	strictSchemaTypeObject = "object"
	strictSchemaTypeString = "string"
	strictSchemaTypeNull   = "null"
	strictSchemaResource   = "schema://goa-ai/openai/canonical.json"
	strictSchemaMaxDepth   = 10
	strictSchemaMaxEnums   = 1_000
	strictSchemaMaxProps   = 5_000
	strictSchemaMaxStrings = 120_000
	strictSchemaMaxEnumStr = 15_000
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
	strictChildSchemaMapKeywords = []string{"properties", "patternProperties", "$defs", "definitions"}

	// strictAllowedKeywords is the JSON Schema subset accepted by OpenAI
	// strict mode after this adapter removes annotations and rewrites oneOf.
	strictAllowedKeywords = map[string]struct{}{
		"$defs":                {},
		"$ref":                 {},
		"additionalProperties": {},
		"anyOf":                {},
		"const":                {},
		"description":          {},
		"enum":                 {},
		"exclusiveMaximum":     {},
		"exclusiveMinimum":     {},
		"format":               {},
		"items":                {},
		"maximum":              {},
		"maxItems":             {},
		"maxLength":            {},
		"minimum":              {},
		"minLength":            {},
		"minItems":             {},
		"multipleOf":           {},
		"pattern":              {},
		"patternProperties":    {},
		"properties":           {},
		"required":             {},
		"title":                {},
		"type":                 {},
	}

	// fineTunedUnsupportedKeywords are valid for base OpenAI models but are
	// rejected by Structured Outputs on model IDs beginning with "ft:".
	fineTunedUnsupportedKeywords = map[string]struct{}{
		"format":            {},
		"maxItems":          {},
		"maxLength":         {},
		"maximum":           {},
		"minItems":          {},
		"minLength":         {},
		"minimum":           {},
		"multipleOf":        {},
		"pattern":           {},
		"patternProperties": {},
	}
)

type (
	// strictSchemaProjection contains the provider schema plus the generated
	// paths where OpenAI may emit null only because strict mode made an
	// optional canonical member required.
	strictSchemaProjection struct {
		schema        map[string]any
		root          *strictNullProjection
		canonicalizes bool
	}

	// strictNullProjection describes canonical object and array structure.
	// dropNull is true only for a canonically optional, non-nullable member.
	strictNullProjection struct {
		dropNull   bool
		properties map[string]*strictNullProjection
		item       *strictNullProjection
	}

	// strictSchemaLoader rejects references outside the canonical schema
	// document while nullability is compiled.
	strictSchemaLoader struct{}

	// strictSchemaUsage accumulates OpenAI's document-wide schema limits while
	// the final provider document is checked.
	strictSchemaUsage struct {
		properties  int
		enumValues  int
		stringChars int
	}
)

// Load rejects every external schema reference.
func (strictSchemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not allowed", url)
}

// projectStrictSchema rewrites one canonical JSON Schema document into the
// subset OpenAI strict mode accepts and returns it in the decoded form the SDK
// request types expect. Empty canonical schemas project to the closed empty
// object. Callers wrap returned errors with the owning tool or
// structured-output name.
func projectStrictSchema(schema rawjson.Message) (map[string]any, error) {
	projection, err := compileStrictSchema(schema)
	if err != nil {
		return nil, err
	}
	return projection.schema, nil
}

// compileStrictSchema builds the provider projection and the exact inverse for
// transport-only null members in one pass over the canonical schema.
func compileStrictSchema(schema rawjson.Message) (*strictSchemaProjection, error) {
	return compileStrictSchemaForModel(schema, "")
}

// compileStrictSchemaForModel applies the stricter JSON Schema subset used by
// OpenAI fine-tuned model IDs before returning the provider projection.
func compileStrictSchemaForModel(schema rawjson.Message, modelID string) (*strictSchemaProjection, error) {
	data := bytes.TrimSpace(schema)
	if len(data) == 0 {
		return &strictSchemaProjection{
			schema: map[string]any{"type": strictSchemaTypeObject, "additionalProperties": false},
		}, nil
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
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(strictSchemaLoader{})
	if err := compiler.AddResource(strictSchemaResource, doc); err != nil {
		return nil, fmt.Errorf("add canonical schema: %w", err)
	}
	if _, err := compiler.Compile(strictSchemaResource); err != nil {
		return nil, fmt.Errorf("compile canonical schema: %w", err)
	}
	nulls, err := buildStrictNullProjection(
		doc,
		doc,
		compiler,
		strictSchemaResource+"#",
		make(map[string]*strictNullProjection),
	)
	if err != nil {
		return nil, err
	}
	if err := projectStrictNode(doc, "$", compiler, strictSchemaResource+"#"); err != nil {
		return nil, err
	}
	if strings.HasPrefix(modelID, "ft:") {
		if err := validateFineTunedStrictSchemaNode(doc, "$"); err != nil {
			return nil, err
		}
	}
	if err := validateStrictSchemaSubset(doc); err != nil {
		return nil, err
	}
	return &strictSchemaProjection{
		schema:        doc,
		root:          nulls,
		canonicalizes: strictProjectionDropsNulls(nulls),
	}, nil
}

// strictProjectionDropsNulls reports whether the provider schema can emit a
// null member that the canonical tool payload must omit.
func strictProjectionDropsNulls(projection *strictNullProjection) bool {
	if projection == nil {
		return false
	}
	if projection.dropNull || strictProjectionDropsNulls(projection.item) {
		return true
	}
	for _, child := range projection.properties {
		if strictProjectionDropsNulls(child) {
			return true
		}
	}
	return false
}

// validateStrictSchemaSubset rejects provider documents that exceed OpenAI's
// strict-schema keywords or document-wide size limits.
func validateStrictSchemaSubset(root map[string]any) error {
	if _, ok := root["anyOf"]; ok {
		return errors.New("schema root cannot use anyOf in OpenAI strict mode")
	}
	usage := &strictSchemaUsage{}
	return validateStrictSchemaNode(root, "$", 1, usage)
}

// validateStrictSchemaNode checks one schema node and recursively charges the
// property names, definition names, and enum strings OpenAI limits.
func validateStrictSchemaNode(node map[string]any, path string, depth int, usage *strictSchemaUsage) error {
	if depth > strictSchemaMaxDepth {
		return fmt.Errorf("schema at %s exceeds OpenAI's maximum nesting depth %d", path, strictSchemaMaxDepth)
	}
	for keyword := range node {
		if _, ok := strictAllowedKeywords[keyword]; !ok {
			return fmt.Errorf("schema at %s uses unsupported OpenAI strict-mode keyword %q", path, keyword)
		}
	}
	if values, ok := node["enum"].([]any); ok {
		usage.enumValues += len(values)
		if usage.enumValues > strictSchemaMaxEnums {
			return fmt.Errorf("schema exceeds OpenAI's maximum of %d enum values", strictSchemaMaxEnums)
		}
		enumChars := 0
		for _, value := range values {
			if text, ok := value.(string); ok {
				chars := utf8.RuneCountInString(text)
				enumChars += chars
				usage.stringChars += chars
			}
		}
		if len(values) > 250 && enumChars > strictSchemaMaxEnumStr {
			return fmt.Errorf(
				"schema enum at %s exceeds OpenAI's %d-character limit for more than 250 values",
				path,
				strictSchemaMaxEnumStr,
			)
		}
	}
	if value, ok := node["const"].(string); ok {
		usage.stringChars += utf8.RuneCountInString(value)
	}
	if properties, ok := node["properties"].(map[string]any); ok {
		usage.properties += len(properties)
		if usage.properties > strictSchemaMaxProps {
			return fmt.Errorf("schema exceeds OpenAI's maximum of %d object properties", strictSchemaMaxProps)
		}
		for name, rawChild := range properties {
			usage.stringChars += utf8.RuneCountInString(name)
			child, ok := rawChild.(map[string]any)
			if !ok {
				return fmt.Errorf("schema property %s.%s is not an object", path, name)
			}
			if err := validateStrictSchemaNode(child, path+".properties."+name, depth+1, usage); err != nil {
				return err
			}
		}
	}
	if patterns, ok := node["patternProperties"].(map[string]any); ok {
		for pattern, rawChild := range patterns {
			usage.stringChars += utf8.RuneCountInString(pattern)
			child, ok := rawChild.(map[string]any)
			if !ok {
				return fmt.Errorf("schema pattern property %s.%s is not an object", path, pattern)
			}
			if err := validateStrictSchemaNode(
				child,
				path+".patternProperties."+pattern,
				depth+1,
				usage,
			); err != nil {
				return err
			}
		}
	}
	if item, ok := node["items"].(map[string]any); ok {
		if err := validateStrictSchemaNode(item, path+".items", depth+1, usage); err != nil {
			return err
		}
	}
	if branches, ok := node["anyOf"].([]any); ok {
		for index, rawBranch := range branches {
			branch, ok := rawBranch.(map[string]any)
			if !ok {
				return fmt.Errorf("schema branch %s.anyOf[%d] is not an object", path, index)
			}
			if err := validateStrictSchemaNode(branch, fmt.Sprintf("%s.anyOf[%d]", path, index), depth, usage); err != nil {
				return err
			}
		}
	}
	if definitions, ok := node["$defs"].(map[string]any); ok {
		for name, rawDefinition := range definitions {
			usage.stringChars += utf8.RuneCountInString(name)
			definition, ok := rawDefinition.(map[string]any)
			if !ok {
				return fmt.Errorf("schema definition %s.$defs.%s is not an object", path, name)
			}
			if err := validateStrictSchemaNode(definition, path+".$defs."+name, depth, usage); err != nil {
				return err
			}
		}
	}
	if usage.stringChars > strictSchemaMaxStrings {
		return fmt.Errorf(
			"schema exceeds OpenAI's %d-character limit for names, enum values, and constants",
			strictSchemaMaxStrings,
		)
	}
	return nil
}

// validateFineTunedStrictSchemaNode rejects constraints that OpenAI documents
// as unavailable on fine-tuned models while leaving base-model schemas intact.
func validateFineTunedStrictSchemaNode(node map[string]any, path string) error {
	for keyword := range node {
		if _, unsupported := fineTunedUnsupportedKeywords[keyword]; unsupported {
			return fmt.Errorf(
				"schema at %s uses keyword %q, which OpenAI fine-tuned models do not support",
				path,
				keyword,
			)
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		if err := validateFineTunedStrictSchemaNode(items, path+".items"); err != nil {
			return err
		}
	}
	for _, keyword := range strictChildSchemaListKeywords {
		branches, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		for index, branch := range branches {
			child, ok := branch.(map[string]any)
			if !ok {
				continue
			}
			if err := validateFineTunedStrictSchemaNode(
				child,
				fmt.Sprintf("%s.%s[%d]", path, keyword, index),
			); err != nil {
				return err
			}
		}
	}
	for _, keyword := range strictChildSchemaMapKeywords {
		children, ok := node[keyword].(map[string]any)
		if !ok {
			continue
		}
		for name, rawChild := range children {
			child, ok := rawChild.(map[string]any)
			if !ok {
				continue
			}
			if err := validateFineTunedStrictSchemaNode(
				child,
				path+"."+keyword+"."+name,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// projectStrictNode rewrites one schema node in place and recurses through the
// keyword positions that hold child schemas. Recursion is keyword-driven so
// instance data such as enum values is never mistaken for schema. path names
// the node in rejection errors.
func projectStrictNode(node map[string]any, path string, compiler *jsonschema.Compiler, location string) error {
	for _, keyword := range strictUnsupportedKeywords {
		delete(node, keyword)
	}
	projectStrictFormat(node)
	if err := projectStrictUnion(node, path); err != nil {
		return err
	}
	if isBranchOnlyObjectUnion(node) {
		delete(node, "type")
		delete(node, "additionalProperties")
		delete(node, "required")
	} else if includesSchemaType(node, strictSchemaTypeObject) {
		if err := projectStrictObject(node, path, compiler, location); err != nil {
			return err
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		if err := projectStrictNode(items, path+".items", compiler, location+"/items"); err != nil {
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
			if err := projectStrictNode(
				branchMap,
				fmt.Sprintf("%s.%s[%d]", path, keyword, i),
				compiler,
				fmt.Sprintf("%s/%s/%d", location, keyword, i),
			); err != nil {
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
			if err := projectStrictNode(
				childMap,
				path+"."+keyword+"."+name,
				compiler,
				location+"/"+keyword+"/"+escapeJSONPointerToken(name),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// isBranchOnlyObjectUnion reports a wrapper whose object shape is defined only
// by union branches. Closing the wrapper itself would reject every property
// declared by those branches.
func isBranchOnlyObjectUnion(node map[string]any) bool {
	if !includesSchemaType(node, strictSchemaTypeObject) {
		return false
	}
	if _, hasProperties := node["properties"]; hasProperties {
		return false
	}
	_, hasUnion := node["anyOf"]
	return hasUnion
}

// projectStrictObject enforces the strict closed-object contract: objects
// declare additionalProperties:false and every property is required, with
// canonically optional properties made nullable so the model can still omit
// them by emitting null.
func projectStrictObject(node map[string]any, path string, compiler *jsonschema.Compiler, location string) error {
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
			acceptsNull, err := schemaAcceptsNull(
				compiler,
				location+"/properties/"+escapeJSONPointerToken(name),
			)
			if err != nil {
				return err
			}
			if !acceptsNull {
				makeStrictNullable(property)
			}
		}
	}
	allRequired := make([]any, len(names))
	for i, name := range names {
		allRequired[i] = name
	}
	node["required"] = allRequired
	return nil
}

// projectStrictUnion folds oneOf into anyOf only when every pair of branches
// is provably disjoint. In that case the two keywords accept the same values.
// Ambiguous branches are rejected before a provider call because widening them
// would let OpenAI generate a value that the canonical codec rejects.
func projectStrictUnion(node map[string]any, path string) error {
	branches, ok := node["oneOf"].([]any)
	if !ok {
		return nil
	}
	if !strictUnionBranchesAreDisjoint(branches) {
		return fmt.Errorf(
			"schema at %s uses oneOf branches that may overlap; OpenAI strict mode cannot preserve exclusive union semantics",
			path,
		)
	}
	if _, exists := node["anyOf"]; exists {
		return fmt.Errorf(
			"schema at %s combines oneOf with anyOf; OpenAI strict mode cannot preserve both constraints",
			path,
		)
	}
	delete(node, "oneOf")
	node["anyOf"] = branches
	return nil
}

// strictUnionBranchesAreDisjoint proves that no JSON value can satisfy two
// branches. It recognizes disjoint JSON types and Goa's generated object
// unions, whose required discriminator property has distinct string values.
func strictUnionBranchesAreDisjoint(branches []any) bool {
	if len(branches) < 2 {
		return false
	}
	for left := range branches {
		leftBranch, ok := branches[left].(map[string]any)
		if !ok {
			return false
		}
		for right := left + 1; right < len(branches); right++ {
			rightBranch, ok := branches[right].(map[string]any)
			if !ok || !strictBranchesAreDisjoint(leftBranch, rightBranch) {
				return false
			}
		}
	}
	return true
}

// strictBranchesAreDisjoint recognizes the two exact forms whose separation
// the adapter can prove without interpreting the complete JSON Schema language.
func strictBranchesAreDisjoint(left, right map[string]any) bool {
	leftTypes, leftTyped := strictSchemaTypes(left)
	rightTypes, rightTyped := strictSchemaTypes(right)
	if leftTyped && rightTyped && !strictSchemaTypesOverlap(leftTypes, rightTypes) {
		return true
	}
	if !includesSchemaType(left, strictSchemaTypeObject) ||
		!includesSchemaType(right, strictSchemaTypeObject) {
		return false
	}
	leftRequired := strictRequiredProperties(left)
	rightRequired := strictRequiredProperties(right)
	leftProperties, leftOK := left["properties"].(map[string]any)
	rightProperties, rightOK := right["properties"].(map[string]any)
	if !leftOK || !rightOK {
		return false
	}
	for name := range leftRequired {
		if _, required := rightRequired[name]; !required {
			continue
		}
		leftValues, leftOK := strictStringValues(leftProperties[name])
		rightValues, rightOK := strictStringValues(rightProperties[name])
		if leftOK && rightOK && !strictStringValuesOverlap(leftValues, rightValues) {
			return true
		}
	}
	return false
}

// strictSchemaTypes returns the explicit JSON types declared by one branch.
func strictSchemaTypes(node map[string]any) (map[string]struct{}, bool) {
	types := make(map[string]struct{})
	switch value := node["type"].(type) {
	case string:
		types[value] = struct{}{}
	case []any:
		for _, item := range value {
			name, ok := item.(string)
			if !ok {
				return nil, false
			}
			types[name] = struct{}{}
		}
	default:
		return nil, false
	}
	return types, len(types) > 0
}

// strictSchemaTypesOverlap accounts for integer values also satisfying number.
func strictSchemaTypesOverlap(left, right map[string]struct{}) bool {
	for leftType := range left {
		for rightType := range right {
			if leftType == rightType ||
				(leftType == "integer" && rightType == "number") ||
				(leftType == "number" && rightType == "integer") {
				return true
			}
		}
	}
	return false
}

// strictRequiredProperties returns the property names every matching object
// must contain.
func strictRequiredProperties(node map[string]any) map[string]struct{} {
	required := make(map[string]struct{})
	names, _ := node["required"].([]any)
	for _, value := range names {
		if name, ok := value.(string); ok {
			required[name] = struct{}{}
		}
	}
	return required
}

// strictStringValues returns the finite string values accepted by a
// discriminator property.
func strictStringValues(raw any) (map[string]struct{}, bool) {
	node, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	if value, ok := node["const"].(string); ok {
		return map[string]struct{}{value: {}}, true
	}
	values, ok := node["enum"].([]any)
	if !ok || len(values) == 0 {
		return nil, false
	}
	result := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok {
			return nil, false
		}
		result[value] = struct{}{}
	}
	return result, true
}

// strictStringValuesOverlap reports whether two finite discriminator sets
// share a value.
func strictStringValuesOverlap(left, right map[string]struct{}) bool {
	for value := range left {
		if _, exists := right[value]; exists {
			return true
		}
	}
	return false
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

// makeStrictNullable wraps one complete canonical property schema with a null
// branch. Keeping every original keyword in the non-null branch preserves
// interactions such as type plus const and typed allOf constraints.
func makeStrictNullable(property map[string]any) {
	original := make(map[string]any, len(property))
	for key, value := range property {
		original[key] = value
		delete(property, key)
	}
	property["anyOf"] = []any{
		original,
		map[string]any{"type": strictSchemaTypeNull},
	}
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

// buildStrictNullProjection compiles canonical schema structure before the
// provider projection adds transport-only null branches.
func buildStrictNullProjection(
	node, root map[string]any,
	compiler *jsonschema.Compiler,
	location string,
	refs map[string]*strictNullProjection,
) (*strictNullProjection, error) {
	if ref, ok := node["$ref"].(string); ok {
		if cached := refs[ref]; cached != nil {
			return cached, nil
		}
		target, err := resolveLocalSchemaRef(root, ref)
		if err != nil {
			return nil, err
		}
		placeholder := &strictNullProjection{}
		refs[ref] = placeholder
		resolved, err := buildStrictNullProjection(target, root, compiler, strictSchemaResource+ref, refs)
		if err != nil {
			return nil, err
		}
		*placeholder = *resolved
		return placeholder, nil
	}

	projection := &strictNullProjection{}
	if properties, ok := node["properties"].(map[string]any); ok {
		required := schemaRequiredNames(node)
		projection.properties = make(map[string]*strictNullProjection, len(properties))
		for name, rawProperty := range properties {
			property, ok := rawProperty.(map[string]any)
			if !ok {
				continue
			}
			childLocation := location + "/properties/" + escapeJSONPointerToken(name)
			child, err := buildStrictNullProjection(property, root, compiler, childLocation, refs)
			if err != nil {
				return nil, err
			}
			_, isRequired := required[name]
			acceptsNull, err := schemaAcceptsNull(compiler, childLocation)
			if err != nil {
				return nil, err
			}
			projection.properties[name] = &strictNullProjection{
				dropNull:   !isRequired && !acceptsNull,
				properties: child.properties,
				item:       child.item,
			}
		}
	}
	if item, ok := node["items"].(map[string]any); ok {
		child, err := buildStrictNullProjection(item, root, compiler, location+"/items", refs)
		if err != nil {
			return nil, err
		}
		projection.item = child
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		branches, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		for index, rawBranch := range branches {
			branch, ok := rawBranch.(map[string]any)
			if !ok {
				continue
			}
			child, err := buildStrictNullProjection(
				branch,
				root,
				compiler,
				fmt.Sprintf("%s/%s/%d", location, keyword, index),
				refs,
			)
			if err != nil {
				return nil, err
			}
			if err := mergeStrictNullProjection(
				projection,
				child,
				fmt.Sprintf("%s/%s/%d", location, keyword, index),
			); err != nil {
				return nil, err
			}
		}
	}
	return projection, nil
}

// schemaRequiredNames returns the object members required by the canonical
// schema node.
func schemaRequiredNames(node map[string]any) map[string]struct{} {
	required := make(map[string]struct{})
	names, _ := node["required"].([]any)
	for _, raw := range names {
		if name, ok := raw.(string); ok {
			required[name] = struct{}{}
		}
	}
	return required
}

// schemaAcceptsNull asks the compiled canonical schema whether null is valid at
// one exact property location. This covers references and every composition
// keyword without reimplementing JSON Schema semantics.
func schemaAcceptsNull(compiler *jsonschema.Compiler, location string) (bool, error) {
	schema, err := compiler.Compile(location)
	if err != nil {
		return false, fmt.Errorf("compile canonical property schema %q: %w", location, err)
	}
	return schema.Validate(nil) == nil, nil
}

// escapeJSONPointerToken encodes one property name for a schema fragment URI.
func escapeJSONPointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// resolveLocalSchemaRef resolves one JSON Pointer within the same generated
// schema document.
func resolveLocalSchemaRef(root map[string]any, ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("strict schema reference %q is not local", ref)
	}
	var current any = root
	for _, encoded := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		name := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("strict schema reference %q does not resolve to an object", ref)
		}
		current, ok = object[name]
		if !ok {
			return nil, fmt.Errorf("strict schema reference %q is missing %q", ref, name)
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("strict schema reference %q does not resolve to a schema", ref)
	}
	return resolved, nil
}

// mergeStrictNullProjection combines union branches without introducing
// model-dependent choices.
func mergeStrictNullProjection(dst, src *strictNullProjection, path string) error {
	if src == nil {
		return nil
	}
	if src.item != nil {
		if dst.item == nil {
			dst.item = src.item
		} else {
			if err := mergeStrictNullProjection(dst.item, src.item, path+"/items"); err != nil {
				return err
			}
		}
	}
	if len(src.properties) == 0 {
		return nil
	}
	if dst.properties == nil {
		dst.properties = make(map[string]*strictNullProjection, len(src.properties))
	}
	for name, source := range src.properties {
		if target := dst.properties[name]; target != nil {
			if target.dropNull != source.dropNull {
				return fmt.Errorf(
					"openai: strict schema branches require conflicting null handling at %s/properties/%s",
					path,
					escapeJSONPointerToken(name),
				)
			}
			if err := mergeStrictNullProjection(
				target,
				source,
				path+"/properties/"+escapeJSONPointerToken(name),
			); err != nil {
				return err
			}
			continue
		}
		dst.properties[name] = source
	}
	return nil
}

// canonicalize removes only null object members introduced by OpenAI's strict
// all-members-required projection. Provider JSON is returned unchanged when no
// such member is present.
func (p *strictSchemaProjection) canonicalize(data []byte) (rawjson.Message, error) {
	if p == nil || p.root == nil {
		return slices.Clone(data), nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	changed, err := removeStrictProjectionNulls(value, p.root, 0)
	if err != nil {
		return nil, err
	}
	if !changed {
		return slices.Clone(data), nil
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// removeStrictProjectionNulls applies the compiled inverse to one provider
// value. The depth bound matches generated model-output traversal limits.
func removeStrictProjectionNulls(value any, projection *strictNullProjection, depth int) (bool, error) {
	if projection == nil {
		return false, nil
	}
	if depth > 128 {
		return false, errors.New("openai: strict output exceeds maximum nesting depth 128")
	}
	changed := false
	switch actual := value.(type) {
	case []any:
		for _, item := range actual {
			itemChanged, err := removeStrictProjectionNulls(item, projection.item, depth+1)
			if err != nil {
				return false, err
			}
			changed = changed || itemChanged
		}
	case map[string]any:
		for name, child := range projection.properties {
			member, present := actual[name]
			if !present {
				continue
			}
			if member == nil && child.dropNull {
				delete(actual, name)
				changed = true
				continue
			}
			memberChanged, err := removeStrictProjectionNulls(member, child, depth+1)
			if err != nil {
				return false, err
			}
			changed = changed || memberChanged
		}
	}
	return changed, nil
}

// containsSchemaTypeName reports whether a type union names the given type.
func containsSchemaTypeName(types []any, want string) bool {
	return slices.ContainsFunc(types, func(entry any) bool {
		name, ok := entry.(string)
		return ok && name == want
	})
}
