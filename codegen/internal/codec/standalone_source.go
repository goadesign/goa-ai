// Package codec emits JSON readers that reject malformed text and duplicate members
// before generated tool and original-value codecs decode their typed values.
package codec

// StrictJSONReaderSource emits the shared JSON reader that rejects duplicate members
// and malformed Unicode before generated codecs check or convert the value.
const StrictJSONReaderSource = `{{define "strict-json-reader"}}
// {{ $.ReadStrictJSON }} checks the complete JSON document for malformed text and
// duplicate members, then returns values with their original number text.
func {{ $.ReadStrictJSON }}(data []byte) (any, error) {
	if err := {{ $.ValidateJSONText }}(data); err != nil { return nil, err }
	if !{{ $.Imports.JSON }}.Valid(data) {
		// When two complete JSON values arrive, keep the caller's distinct
		// multiple-document error instead of reporting malformed syntax.
		decoder := {{ $.Imports.JSON }}.NewDecoder({{ $.Imports.Bytes }}.NewReader(data))
		var document {{ $.Imports.JSON }}.RawMessage
		if err := decoder.Decode(&document); err == nil {
			if err := decoder.Decode(&document); err == nil {
				return nil, {{ $.Imports.Fmt }}.Errorf("multiple JSON documents")
			}
		}
		return nil, {{ $.Imports.Fmt }}.Errorf("invalid JSON syntax or nesting")
	}
	decoder := {{ $.Imports.JSON }}.NewDecoder({{ $.Imports.Bytes }}.NewReader(data))
	decoder.UseNumber()
	return {{ $.ReadJSONValue }}(decoder)
}

// {{ $.ReadJSONValue }} reads one JSON value and rejects repeated object member
// names, returning the value without replacing earlier members.
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

// {{ $.ValidateJSONText }} checks JSON bytes for invalid text and unpaired Unicode
// escapes, returning an error before encoding/json can replace those characters.
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

{{end}}`

const standaloneSource = `
{{template "strict-json-reader" .}}

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
