// Package codec checks standalone JSON syntax before decoding can replace malformed text
// or overwrite duplicate members. They contain no application schema.
package codec

const standaloneSource = `
// {{ $.ReadStrictJSON }} checks syntax and nesting before walking for duplicate keys,
// and preserves integer text for the generated type checks.
func {{ $.ReadStrictJSON }}(data []byte) (any, error) {
	if err := {{ $.ValidateJSONText }}(data); err != nil { return nil, err }
	if !{{ $.Imports.JSON }}.Valid(data) { return nil, {{ $.Imports.Fmt }}.Errorf("invalid JSON syntax or nesting") }
	decoder := {{ $.Imports.JSON }}.NewDecoder({{ $.Imports.Bytes }}.NewReader(data))
	decoder.UseNumber()
	return {{ $.ReadJSONValue }}(decoder)
}

// {{ $.ReadJSONValue }} checks duplicate decoded keys before storing any object member.
func {{ $.ReadJSONValue }}(decoder *{{ $.Imports.JSON }}.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil { return nil, err }
	switch token {
	case {{ $.Imports.JSON }}.Delim('{'):
		object := make(map[string]any)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil { return nil, err }
			key, ok := token.(string)
			if !ok { return nil, {{ $.Imports.Fmt }}.Errorf("object member name must be a string") }
			if _, exists := object[key]; exists { return nil, {{ $.Imports.Fmt }}.Errorf("duplicate JSON member %q", key) }
			value, err := {{ $.ReadJSONValue }}(decoder)
			if err != nil { return nil, err }
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil { return nil, err }
		return object, nil
	case {{ $.Imports.JSON }}.Delim('['):
		array := make([]any, 0)
		for decoder.More() {
			value, err := {{ $.ReadJSONValue }}(decoder)
			if err != nil { return nil, err }
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil { return nil, err }
		return array, nil
	default:
		if _, delimiter := token.({{ $.Imports.JSON }}.Delim); delimiter { return nil, {{ $.Imports.Fmt }}.Errorf("unexpected JSON delimiter %v", token) }
		return token, nil
	}
}

// {{ $.ValidateJSONText }} rejects invalid UTF-8 and unpaired UTF-16 escapes before
// encoding/json can silently replace them. JSON grammar remains decoder-owned.
func {{ $.ValidateJSONText }}(data []byte) error {
	if !{{ $.Imports.UTF8 }}.Valid(data) { return {{ $.Imports.Fmt }}.Errorf("invalid UTF-8 in JSON") }
	for i := 0; i < len(data); i++ {
		if data[i] != '"' { continue }
		i++
		for ; i < len(data) && data[i] != '"'; i++ {
			if data[i] != '\\' { continue }
			i++
			if i >= len(data) || data[i] != 'u' { continue }
			if i+4 >= len(data) { return {{ $.Imports.Fmt }}.Errorf("incomplete Unicode escape") }
			code, err := {{ $.Imports.Strconv }}.ParseUint(string(data[i+1:i+5]), 16, 16)
			if err != nil { return {{ $.Imports.Fmt }}.Errorf("invalid Unicode escape: %w", err) }
			i += 4
			if code >= 0xdc00 && code <= 0xdfff { return {{ $.Imports.Fmt }}.Errorf("unpaired low Unicode surrogate") }
			if code < 0xd800 || code > 0xdbff { continue }
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return {{ $.Imports.Fmt }}.Errorf("unpaired high Unicode surrogate")
			}
			low, err := {{ $.Imports.Strconv }}.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff { return {{ $.Imports.Fmt }}.Errorf("unpaired high Unicode surrogate") }
			i += 6
		}
	}
	return nil
}

// {{ $.JSONNames.InvalidFieldType }} is the value-codec adapter for shared checks.
func {{ $.JSONNames.InvalidFieldType }}(field, expected, actual, _ string) error {
	return {{ $.Imports.Fmt }}.Errorf("%s: expected %s, got %s", field, expected, actual)
}

// {{ $.JSONNames.UnknownField }} reports exact authored names, without case folding.
func {{ $.JSONNames.UnknownField }}(path, key string, _ []string) error {
	return {{ $.Imports.Fmt }}.Errorf("%s: unknown JSON field %q", path, key)
}

// {{ $.JSONNames.DecodedType }} describes values produced only by the strict JSON reader.
func {{ $.JSONNames.DecodedType }}(value any) string {
	switch value.(type) {
	case nil: return "null"
	case bool: return "boolean"
	case string: return "string"
	case {{ $.Imports.JSON }}.Number: return "number"
	case []any: return "array"
	case map[string]any: return "object"
	default: return "invalid JSON value"
	}
}

// {{ $.JSONNames.ChildPath }} appends an unambiguous quoted member or array index.
func {{ $.JSONNames.ChildPath }}(path, key string, _ bool) string {
	return path + "[" + {{ $.Imports.Strconv }}.Quote(key) + "]"
}
`
