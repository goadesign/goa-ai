// Package codec checks standalone JSON syntax before decoding can replace malformed text
// or overwrite duplicate members. They contain no application schema.
package codec

const standaloneSource = `
// readStrictJSON checks syntax and nesting before walking for duplicate keys,
// and preserves integer text for the generated type checks.
func readStrictJSON(data []byte) (any, error) {
	if err := validateJSONText(data); err != nil { return nil, err }
	if !json.Valid(data) { return nil, fmt.Errorf("invalid JSON syntax or nesting") }
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return readJSONValue(decoder)
}

// readJSONValue checks duplicate decoded keys before storing any object member.
func readJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil { return nil, err }
	switch token {
	case json.Delim('{'):
		object := make(map[string]any)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil { return nil, err }
			key, ok := token.(string)
			if !ok { return nil, fmt.Errorf("object member name must be a string") }
			if _, exists := object[key]; exists { return nil, fmt.Errorf("duplicate JSON member %q", key) }
			value, err := readJSONValue(decoder)
			if err != nil { return nil, err }
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil { return nil, err }
		return object, nil
	case json.Delim('['):
		array := make([]any, 0)
		for decoder.More() {
			value, err := readJSONValue(decoder)
			if err != nil { return nil, err }
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil { return nil, err }
		return array, nil
	default:
		if _, delimiter := token.(json.Delim); delimiter { return nil, fmt.Errorf("unexpected JSON delimiter %v", token) }
		return token, nil
	}
}

// validateJSONText rejects invalid UTF-8 and unpaired UTF-16 escapes before
// encoding/json can silently replace them. JSON grammar remains decoder-owned.
func validateJSONText(data []byte) error {
	if !utf8.Valid(data) { return fmt.Errorf("invalid UTF-8 in JSON") }
	for i := 0; i < len(data); i++ {
		if data[i] != '"' { continue }
		i++
		for ; i < len(data) && data[i] != '"'; i++ {
			if data[i] != '\\' { continue }
			i++
			if i >= len(data) || data[i] != 'u' { continue }
			if i+4 >= len(data) { return fmt.Errorf("incomplete Unicode escape") }
			code, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			if err != nil { return fmt.Errorf("invalid Unicode escape: %w", err) }
			i += 4
			if code >= 0xdc00 && code <= 0xdfff { return fmt.Errorf("unpaired low Unicode surrogate") }
			if code < 0xd800 || code > 0xdbff { continue }
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return fmt.Errorf("unpaired high Unicode surrogate")
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff { return fmt.Errorf("unpaired high Unicode surrogate") }
			i += 6
		}
	}
	return nil
}

// invalidGeneratedFieldTypeError is the value-codec adapter for shared checks.
func invalidGeneratedFieldTypeError(field, expected, actual, _ string) error {
	return fmt.Errorf("%s: expected %s, got %s", field, expected, actual)
}

// unknownJSONFieldError reports exact authored names, without case folding.
func unknownJSONFieldError(path, key string, _ []string) error {
	return fmt.Errorf("%s: unknown JSON field %q", path, key)
}

// decodedJSONType describes values produced only by the strict JSON reader.
func decodedJSONType(value any) string {
	switch value.(type) {
	case nil: return "null"
	case bool: return "boolean"
	case string: return "string"
	case json.Number: return "number"
	case []any: return "array"
	case map[string]any: return "object"
	default: return "invalid JSON value"
	}
}

// generatedJSONChildPath appends an unambiguous quoted member or array index.
func generatedJSONChildPath(path, key string, _ bool) string {
	return path + "[" + strconv.Quote(key) + "]"
}
`
