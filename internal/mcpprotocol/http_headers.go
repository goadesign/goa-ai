// Package mcpprotocol encodes HTTP header values and extracts annotated tool arguments.
// Only statically reachable scalar properties may be mirrored. Integer bounds
// apply to one mirrored property, never to the whole tool payload or operation.
package mcpprotocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"
)

type (
	// HeaderBinding identifies a primitive object property copied to Mcp-Param-.
	HeaderBinding struct {
		// Name is the token appended to the protocol header prefix.
		Name string
		// Path contains the successive JSON object property names.
		Path []string
		// Type is string, integer or boolean.
		Type string
	}
)

const schemaProperties = "properties"

// EncodeHeaderValue preserves unsafe or sentinel-looking strings with the
// protocol's standard-base64 envelope rather than trimming user input.
func EncodeHeaderValue(value string) string {
	unsafe := strings.HasPrefix(value, "=?base64?") || strings.TrimSpace(value) != value
	for i := range len(value) {
		if value[i] < 0x20 || value[i] > 0x7e {
			unsafe = true
		}
	}
	if unsafe {
		return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
	}
	return value
}

// DecodeHeaderValue returns the original UTF-8 text or rejects a malformed encoded header.
func DecodeHeaderValue(value string) (string, error) {
	if !strings.HasPrefix(value, "=?base64?") {
		return value, nil
	}
	if !strings.HasSuffix(value, "?=") {
		return "", errors.New("invalid base64 header envelope")
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(value, "=?base64?"), "?=")
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || !utf8.Valid(decoded) {
		return "", errors.New("invalid base64 UTF-8 header")
	}
	return string(decoded), nil
}

// ParameterValues decodes only the object paths selected by the schema and
// converts each present scalar into its exact header representation.
func ParameterValues(raw json.RawMessage, bindings []HeaderBinding) (map[string]string, error) {
	values := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		value := raw
		for _, property := range binding.Path {
			if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				value = nil
				break
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(value, &object); err != nil || object == nil {
				return nil, fmt.Errorf("header property %q needs an object parent", property)
			}
			value = object[property]
		}
		if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		var text string
		switch binding.Type {
		case "string":
			if err := json.Unmarshal(value, &text); err != nil {
				return nil, fmt.Errorf("header %s requires a string", binding.Name)
			}
		case "boolean":
			var boolean bool
			if err := json.Unmarshal(value, &boolean); err != nil {
				return nil, fmt.Errorf("header %s requires a boolean", binding.Name)
			}
			if boolean {
				text = "true"
			} else {
				text = "false"
			}
		case "integer":
			var number json.Number
			decoder := json.NewDecoder(bytes.NewReader(value))
			decoder.UseNumber()
			if err := decoder.Decode(&number); err != nil || bytes.HasPrefix(bytes.TrimSpace(value), []byte(`"`)) {
				return nil, fmt.Errorf("header %s requires an integer", binding.Name)
			}
			rational, ok := new(big.Rat).SetString(number.String())
			if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
				return nil, fmt.Errorf("header %s requires an integer", binding.Name)
			}
			integer := rational.Num().Int64()
			// The protocol bounds a mirrored integer to the inclusive IEEE754 safe
			// range. Larger integers elsewhere in the payload remain valid.
			const maximum = int64(1<<53 - 1)
			if integer < -maximum || integer > maximum {
				return nil, fmt.Errorf("header %s integer is outside the safe range", binding.Name)
			}
			text = rational.Num().String()
		default:
			return nil, fmt.Errorf("unsupported header property type %q", binding.Type)
		}
		values["Mcp-Param-"+binding.Name] = EncodeHeaderValue(text)
	}
	return values, nil
}

// CompileHeaderBindings validates a discovered schema's annotations without
// resolving references or weakening its argument schema.
func CompileHeaderBindings(schema json.RawMessage) ([]HeaderBinding, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(schema, &root); err != nil || root == nil {
		return nil, errors.New("inputSchema must be an object")
	}
	var rootType string
	if json.Unmarshal(root["type"], &rootType) != nil || rootType != "object" {
		return nil, errors.New("inputSchema must declare an object root")
	}
	var bindings []HeaderBinding
	names := make(map[string]bool)
	if err := walkHeaderSchema(root, nil, true, names, &bindings); err != nil {
		return nil, err
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Name < bindings[j].Name })
	return bindings, nil
}

// walkHeaderSchema checks annotations in every schema branch, accepting only
// paths made entirely of properties entries beneath an object root.
func walkHeaderSchema(schema map[string]json.RawMessage, path []string, reachable bool, names map[string]bool, bindings *[]HeaderBinding) error {
	if raw, present := schema["x-mcp-header"]; present {
		var name, kind string
		if json.Unmarshal(raw, &name) != nil || !validHeaderToken(name) {
			return errors.New("x-mcp-header must be an HTTP field-name token")
		}
		if !reachable || len(path) == 0 || schema["$ref"] != nil || schema["allOf"] != nil || schema["anyOf"] != nil || schema["oneOf"] != nil {
			return errors.New("x-mcp-header must be reachable through properties")
		}
		if json.Unmarshal(schema["type"], &kind) != nil || (kind != "string" && kind != "integer" && kind != "boolean") {
			return errors.New("x-mcp-header requires string, integer, or boolean type")
		}
		folded := strings.ToLower(name)
		if names[folded] {
			return errors.New("duplicate x-mcp-header name")
		}
		names[folded] = true
		*bindings = append(*bindings, HeaderBinding{Name: name, Path: append([]string(nil), path...), Type: kind})
	}
	var kind string
	if err := json.Unmarshal(schema["type"], &kind); err != nil {
		kind = ""
	}
	propertiesReachable := reachable && kind == "object" && schema["$ref"] == nil && schema["allOf"] == nil && schema["anyOf"] == nil && schema["oneOf"] == nil
	for _, key := range []string{schemaProperties, "$defs", "definitions", "patternProperties", "dependentSchemas"} {
		var children map[string]json.RawMessage
		if json.Unmarshal(schema[key], &children) != nil {
			continue
		}
		for property, child := range children {
			var nested map[string]json.RawMessage
			if json.Unmarshal(child, &nested) != nil || nested == nil {
				continue
			}
			childPath := path
			if key == schemaProperties {
				childPath = append(append([]string(nil), path...), property)
			}
			if err := walkHeaderSchema(nested, childPath, key == schemaProperties && propertiesReachable, names, bindings); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		var children []json.RawMessage
		if json.Unmarshal(schema[key], &children) != nil {
			continue
		}
		for _, child := range children {
			var nested map[string]json.RawMessage
			if json.Unmarshal(child, &nested) != nil || nested == nil {
				continue
			}
			if err := walkHeaderSchema(nested, path, false, names, bindings); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "not", "if", "then", "else", "contains", "propertyNames"} {
		var nested map[string]json.RawMessage
		if json.Unmarshal(schema[key], &nested) != nil || nested == nil {
			continue
		}
		if err := walkHeaderSchema(nested, path, false, names, bindings); err != nil {
			return err
		}
	}
	return nil
}

func validHeaderToken(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}
