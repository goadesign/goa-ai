{{- range .Types }}
    {{- if .GenerateCodec }}
// {{ .ExportedCodec }} returns a fresh codec for {{ if .Pointer }}*{{ end }}{{ .FullRef }}.
    {{- if .InjectDecodeFunc }}
// Prefer {{ .InjectDecodeFunc }} when decoding tool calls: FromJSON alone
// leaves Inject()-ed fields unset.
    {{- end }}
func {{ .ExportedCodec }}() tools.JSONCodec[{{ if .Pointer }}*{{ end }}{{ .FullRef }}] {
    return tools.JSONCodec[{{ if .Pointer }}*{{ end }}{{ .FullRef }}]{
        ToJSON:   {{ .MarshalFunc }},
        FromJSON: {{ .UnmarshalFunc }},
    }
}
    {{- end }}
{{- end }}

var (
{{- $printed := false }}
{{- range .Types }}
    {{- if .GenerateCodec }}
    {{- if $printed }}

    {{- end }}
    // {{ .GenericCodec }} provides an untyped codec for {{ if .Pointer }}*{{ end }}{{ .FullRef }}.
    {{ .GenericCodec }} = tools.JSONCodec[any]{
        ToJSON: func(v any) ([]byte, error) {
            // Prefer typed marshal when the value matches the expected type.
            if typed, ok := v.({{ if .Pointer }}*{{ end }}{{ .FullRef }}); ok {
                return {{ .MarshalFunc }}(typed)
            }
            return nil, fmt.Errorf("invalid value type for {{ if .Pointer }}*{{ end }}{{ .FullRef }}: %T", v)
        },
        FromJSON: func(data []byte) (any, error) {
            return {{ .UnmarshalFunc }}(data)
        },
    }
    {{- $printed = true }}
    {{- end }}
{{- end }}
)

{{- /* Emit field descriptions map per type if available */ -}}
{{- range .Types }}
{{- if .FieldDescs }}
var {{ goify .TypeName false }}FieldDescs = map[string]string{
    {{- range $k, $v := .FieldDescs }}
    {{ printf "%q" $k }}: {{ printf "%q" $v }},
    {{- end }}
}
{{- end }}
{{- end }}

{{- /* Emit generated JSON type metadata per type if available */ -}}
{{- range .Types }}
{{- if .FieldJSONTypes }}
var {{ goify .TypeName false }}FieldJSONTypes = map[string]string{
    {{- range $k, $v := .FieldJSONTypes }}
    {{ printf "%q" $k }}: {{ printf "%q" $v }},
    {{- end }}
}
{{- end }}
{{- end }}

{{- /* Emit generated closed-object key metadata per type if available */ -}}
{{- range .Types }}
{{- if .FieldAllowedObjectKeys }}
var {{ goify .TypeName false }}FieldAllowedObjectKeys = map[string][]string{
    {{- range $path, $keys := .FieldAllowedObjectKeys }}
    {{ printf "%q" $path }}: {
        {{- range $keys }}
        {{ printf "%q" . }},
        {{- end }}
    },
    {{- end }}
}
{{- end }}
{{- end }}

{{- /* Compute whether any type has transport validation to gate helper emission */ -}}
{{- $hasValidation := false }}
{{- range .Types }}
    {{- if .TransportValidationSrc }}
        {{- $hasValidation = true }}
    {{- end }}
{{- end }}

{{- if $hasValidation }}
// newValidationError converts a goa.ServiceError (possibly merged) into a
// tools.ValidationError with structured FieldIssue entries. It trims any leading
// "body." from field names for conciseness.
func newValidationError(err error) error {
    if err == nil {
        return nil
    }
    var se *goa.ServiceError
    if !errors.As(err, &se) {
        return err
    }
    hist := se.History()
    issues := make([]*tools.FieldIssue, 0, len(hist))
    for _, h := range hist {
        var field string
        if h.Field != nil {
            field = *h.Field
        }
        if strings.HasPrefix(field, "body.") {
            field = strings.TrimPrefix(field, "body.")
        }
        if field == "" {
            field = "$payload"
        }
        issues = append(issues, &tools.FieldIssue{Field: field, Constraint: h.Name})
    }
    if len(issues) == 0 {
        return err
    }
    return tools.NewValidationError(err.Error(), issues, nil)
}
{{- end }}

{{- /* Per-type enrichment attaching descriptions for any type with validation (payload or non-payload) */ -}}
{{- range .Types }}
{{- if and .FieldDescs .TransportValidationSrc (ne (index .TransportValidationSrc 0) "") }}
func enrich{{ .TypeName }}ValidationError(err error) error {
    var ve *tools.ValidationError
    if !errors.As(err, &ve) {
        return err
    }
    issues := ve.Issues()
    if len(issues) == 0 {
        return err
    }
    m := make(map[string]string)
    {{- if .FieldDescs }}
    for _, is := range issues {
        if d, ok := tools.LookupFieldMetadata({{ goify .TypeName false }}FieldDescs, is.Field); ok && d != "" {
            m[is.Field] = d
        }
    }
    {{- end }}
    return tools.NewValidationError(ve.Error(), issues, m)
}
{{- end }}
{{- end }}

{{- range .Types }}
{{- if .FieldJSONTypes }}
func invalid{{ .TypeName }}FieldTypeError(err error) error {
    var typeErr *json.UnmarshalTypeError
    if !errors.As(err, &typeErr) {
        return err
    }
    field := typeErr.Field
    {{- if .TransportTypeName }}
    field = strings.TrimPrefix(field, "{{ .TransportTypeName }}.")
    {{- end }}
    if field == "" {
        field = "$payload"
    }
    expected, ok := tools.LookupFieldMetadata({{ goify .TypeName false }}FieldJSONTypes, field)
    if !ok {
        return err
    }
    actual := generatedUnmarshalJSONType(typeErr.Value)
    if actual == "" {
        return err
    }
    return tools.NewValidationError(
        err.Error(),
        []*tools.FieldIssue{
            {
                Field:            field,
                Constraint:       "invalid_field_type",
                ExpectedJSONType: expected,
                ActualJSONType:   actual,
            },
        },
        nil,
    )
}
{{- end }}
{{- end }}

{{- range .Types }}
    {{- if .GenerateCodec }}
// {{ .MarshalFunc }} serializes {{ if .Pointer }}*{{ end }}{{ .FullRef }} into JSON.
func {{ .MarshalFunc }}(v {{ if .Pointer }}*{{ end }}{{ .FullRef }}) ([]byte, error) {
    {{- if .Pointer }}
    if v == nil {
        {{- if eq .Usage "server-data" }}
        return []byte("null"), nil
        {{- else }}
        return nil, fmt.Errorf("{{ .NilError }}")
        {{- end }}
    }
    {{- end }}
    {{- if .TransportTypeName }}
    in := v
    _ = in
    var out {{ if .TransportPointer }}*{{ end }}toolhttp.{{ .TransportTypeName }}
{{ .EncodeTransform }}
    return json.Marshal(out)
    {{- else }}
    return json.Marshal(v)
    {{- end }}
}

// {{ .UnmarshalFunc }} deserializes JSON into {{ if .Pointer }}*{{ end }}{{ .FullRef }}.
func {{ .UnmarshalFunc }}(data []byte) ({{ if .Pointer }}*{{ end }}{{ .FullRef }}, error) {
    {{- if not .Pointer }}
    var zero {{ if .Pointer }}*{{ end }}{{ .FullRef }}
    {{- end }}
    if len(data) == 0 {
        {{- if and (eq .Usage "payload") .AcceptEmpty }}
        var v {{ if .Pointer }}*{{ end }}{{ .FullRef }}
        return v, nil
        {{- else }}
        {{- if .Pointer }}
        return nil, fmt.Errorf("{{ .EmptyError }}")
        {{- else }}
        return zero, fmt.Errorf("{{ .EmptyError }}")
        {{- end }}
        {{- end }}
    }
    {{- if .TransportTypeName }}
    var tv toolhttp.{{ .TransportTypeName }}
    {{- if .FieldAllowedObjectKeys }}
    if err := decodeKnownJSON(
        data,
        &tv,
        {{ goify .TypeName false }}FieldAllowedObjectKeys,
        {{- if .FieldJSONTypes }}{{ goify .TypeName false }}FieldJSONTypes{{ else }}nil{{ end }},
        {{- if .FieldDescs }}{{ goify .TypeName false }}FieldDescs{{ else }}nil{{ end }},
    ); err != nil {
    {{- else if eq .Usage "payload" }}
    if err := decodeStrictJSON(
        data,
        &tv,
        {{- if .FieldJSONTypes }}{{ goify .TypeName false }}FieldJSONTypes{{ else }}nil{{ end }},
        {{- if .FieldDescs }}{{ goify .TypeName false }}FieldDescs{{ else }}nil{{ end }},
    ); err != nil {
    {{- else if .FieldJSONTypes }}
    if err := decodeKnownJSON(
        data,
        &tv,
        nil,
        {{ goify .TypeName false }}FieldJSONTypes,
        {{- if .FieldDescs }}{{ goify .TypeName false }}FieldDescs{{ else }}nil{{ end }},
    ); err != nil {
    {{- else }}
    if err := json.Unmarshal(data, &tv); err != nil {
    {{- end }}
        {{- if .FieldJSONTypes }}
        {{- if .Pointer }}
        return nil, invalid{{ .TypeName }}FieldTypeError(err)
        {{- else }}
        return zero, invalid{{ .TypeName }}FieldTypeError(err)
        {{- end }}
        {{- else }}
        {{- if .Pointer }}
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- else }}
        return zero, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- end }}
        {{- end }}
    }
    {{- if .TransportValidationSrc }}
    if err := toolhttp.Validate{{ .TransportTypeName }}({{ if .TransportPointer }}&{{ end }}tv); err != nil {
        err = newValidationError(err)
        {{- if .FieldDescs }}
        err = enrich{{ .TypeName }}ValidationError(err)
        {{- end }}
        {{- if .Pointer }}
        return nil, err
        {{- else }}
        return zero, err
        {{- end }}
    }
    {{- end }}
    in := {{ if .TransportPointer }}&{{ end }}tv
    _ = in
    var out {{ if .Pointer }}*{{ end }}{{ .FullRef }}
{{ .DecodeTransform }}
    return out, nil
    {{- else }}
    var v {{ .FullRef }}
    {{- if .FieldAllowedObjectKeys }}
    if err := decodeKnownJSON(
        data,
        &v,
        {{ goify .TypeName false }}FieldAllowedObjectKeys,
        {{- if .FieldJSONTypes }}{{ goify .TypeName false }}FieldJSONTypes{{ else }}nil{{ end }},
        {{- if .FieldDescs }}{{ goify .TypeName false }}FieldDescs{{ else }}nil{{ end }},
    ); err != nil {
    {{- else if eq .Usage "payload" }}
    if err := decodeStrictJSON(
        data,
        &v,
        {{- if .FieldJSONTypes }}{{ goify .TypeName false }}FieldJSONTypes{{ else }}nil{{ end }},
        {{- if .FieldDescs }}{{ goify .TypeName false }}FieldDescs{{ else }}nil{{ end }},
    ); err != nil {
    {{- else if .FieldJSONTypes }}
    if err := decodeKnownJSON(
        data,
        &v,
        nil,
        {{ goify .TypeName false }}FieldJSONTypes,
        {{- if .FieldDescs }}{{ goify .TypeName false }}FieldDescs{{ else }}nil{{ end }},
    ); err != nil {
    {{- else }}
    if err := json.Unmarshal(data, &v); err != nil {
    {{- end }}
        {{- if .FieldJSONTypes }}
        err = invalid{{ .TypeName }}FieldTypeError(err)
        {{- end }}
        {{- if .Pointer }}
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- else }}
        return zero, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- end }}
    }
        {{- if .Pointer }}
    return &v, nil
        {{- else }}
    return v, nil
        {{- end }}
    {{- end }}
}
    {{- end }}
{{- end }}

// decodeStrictJSON decodes one JSON document and rejects null members and
// object fields outside the generated transport contract.
func decodeStrictJSON(
    data []byte,
    v any,
    fieldTypes map[string]string,
    fieldDescriptions map[string]string,
) error {
    if err := validateGeneratedJSON(data, nil, fieldTypes, fieldDescriptions); err != nil {
        return err
    }
    dec := json.NewDecoder(bytes.NewReader(data))
    dec.DisallowUnknownFields()
    if err := dec.Decode(v); err != nil {
        return err
    }
    if err := dec.Decode(&struct{}{}); err != io.EOF {
        return fmt.Errorf("multiple JSON documents")
    }
    return nil
}

// decodeKnownJSON decodes one JSON document after enforcing generated field
// names and JSON categories. Open map fields remain open because their paths
// are absent from the generated closed-object metadata.
func decodeKnownJSON(
    data []byte,
    v any,
    allowed map[string][]string,
    fieldTypes map[string]string,
    fieldDescriptions map[string]string,
) error {
    if err := validateGeneratedJSON(data, allowed, fieldTypes, fieldDescriptions); err != nil {
        return err
    }
    return json.Unmarshal(data, v)
}

// validateGeneratedJSON checks raw JSON facts that Go's typed decoder cannot
// preserve, including the difference between an absent optional member and an
// explicit null.
func validateGeneratedJSON(
    data []byte,
    allowed map[string][]string,
    fieldTypes map[string]string,
    fieldDescriptions map[string]string,
) error {
    var root any
    dec := json.NewDecoder(bytes.NewReader(data))
    dec.UseNumber()
    if err := dec.Decode(&root); err != nil {
        return err
    }
    if err := dec.Decode(&struct{}{}); err != io.EOF {
        return fmt.Errorf("multiple JSON documents")
    }
    return validateGeneratedJSONValue("", "", root, allowed, fieldTypes, fieldDescriptions, true)
}

// validateGeneratedJSONValue walks one decoded value using paths emitted by
// code generation. path names the actual caller field for errors; schemaPath
// uses "*" where a map accepts caller-chosen keys.
func validateGeneratedJSONValue(
    path string,
    schemaPath string,
    value any,
    allowed map[string][]string,
    fieldTypes map[string]string,
    fieldDescriptions map[string]string,
    validateType bool,
) error {
    field := path
    if field == "" {
        field = "$payload"
    }
    schemaField := schemaPath
    if schemaField == "" {
        schemaField = "$payload"
    }
    expected, hasExpectedType := fieldTypes[schemaField]
    description := fieldDescriptions[schemaField]
    if value == nil {
        if hasExpectedType {
            return invalidGeneratedFieldTypeError(field, expected, "null", description)
        }
        return nil
    }
    if validateType {
        actual := decodedJSONType(value)
        if hasExpectedType && !generatedJSONTypesCompatible(expected, actual) {
            return invalidGeneratedFieldTypeError(field, expected, actual, description)
        }
    }
    switch v := value.(type) {
    case []any:
        for _, item := range v {
            if err := validateGeneratedJSONValue(
                path,
                schemaPath,
                item,
                allowed,
                fieldTypes,
                fieldDescriptions,
                false,
            ); err != nil {
                return err
            }
        }
        return nil
    case map[string]any:
        allowedKeys, closed := allowed[schemaPath]
        wildcardPath := "*"
        if schemaPath != "" {
            wildcardPath = schemaPath + ".*"
        }
        mapElements := !closed && hasGeneratedJSONPath(wildcardPath, allowed, fieldTypes)
        keys := make([]string, 0, len(v))
        for key := range v {
            keys = append(keys, key)
        }
        sort.Strings(keys)
        for _, key := range keys {
            if closed && !slices.Contains(allowedKeys, key) {
                return unknownJSONFieldError(path, key, allowedKeys)
            }
            childPath := generatedJSONChildPath(path, key, mapElements)
            childSchemaPath := key
            if schemaPath != "" {
                childSchemaPath = schemaPath + "." + key
            }
            if mapElements {
                childSchemaPath = wildcardPath
            }
            if err := validateGeneratedJSONValue(
                childPath,
                childSchemaPath,
                v[key],
                allowed,
                fieldTypes,
                fieldDescriptions,
                true,
            ); err != nil {
                return err
            }
        }
    }
    return nil
}

// generatedJSONChildPath keeps generated object fields in dotted form until an
// open map introduces caller-chosen keys. From that point it uses JSON Pointer
// so dots, slashes, and tildes in map keys remain unambiguous.
func generatedJSONChildPath(path, key string, mapElement bool) string {
    if mapElement || strings.HasPrefix(path, "/") {
        if path != "" && !strings.HasPrefix(path, "/") {
            path = dottedJSONPathPointer(path)
        }
        return path + "/" + escapeJSONPointerToken(key)
    }
    if path == "" {
        return key
    }
    return path + "." + key
}

// dottedJSONPathPointer converts the generated dotted path leading to an open
// map into JSON Pointer tokens.
func dottedJSONPathPointer(path string) string {
    parts := strings.Split(path, ".")
    var pointer strings.Builder
    for _, part := range parts {
        pointer.WriteByte('/')
        pointer.WriteString(escapeJSONPointerToken(part))
    }
    return pointer.String()
}

// escapeJSONPointerToken applies RFC 6901 escaping to one object key.
func escapeJSONPointerToken(token string) string {
    token = strings.ReplaceAll(token, "~", "~0")
    return strings.ReplaceAll(token, "/", "~1")
}

// decodedJSONType returns the JSON category retained by Decoder.UseNumber.
func decodedJSONType(value any) string {
    switch value.(type) {
    case bool:
        return "boolean"
    case json.Number:
        return "number"
    case string:
        return "string"
    case []any:
        return "array"
    case map[string]any:
        return "object"
    default:
        panic(fmt.Sprintf("unsupported decoded JSON value %T", value))
    }
}

// generatedUnmarshalJSONType reduces encoding/json's detailed value label to
// the stable JSON category carried by FieldIssue.
func generatedUnmarshalJSONType(value string) string {
    category, _, _ := strings.Cut(value, " ")
    switch category {
    case "array", "bool", "number", "object", "string":
        if category == "bool" {
            return "boolean"
        }
        return category
    default:
        return ""
    }
}

// generatedJSONTypesCompatible performs only the raw JSON category check.
// Typed decoding remains responsible for integer syntax, ranges, and formats.
func generatedJSONTypesCompatible(expected, actual string) bool {
    if actual == "number" && (expected == "integer" || expected == "number") {
        return true
    }
    return expected == actual
}

// hasGeneratedJSONPath reports whether code generation emitted metadata for a
// field itself or for a closed object beneath it.
func hasGeneratedJSONPath(
    path string,
    allowed map[string][]string,
    fieldTypes map[string]string,
) bool {
    if _, ok := allowed[path]; ok {
        return true
    }
    if _, ok := fieldTypes[path]; ok {
        return true
    }
    prefix := path + "."
    for candidate := range allowed {
        if strings.HasPrefix(candidate, prefix) {
            return true
        }
    }
    for candidate := range fieldTypes {
        if strings.HasPrefix(candidate, prefix) {
            return true
        }
    }
    return false
}

// invalidGeneratedFieldTypeError reports a raw JSON category that conflicts
// with the generated schema and attaches that schema path's description.
func invalidGeneratedFieldTypeError(field, expected, actual, description string) error {
    var descriptions map[string]string
    if description != "" {
        descriptions = map[string]string{field: description}
    }
    return tools.NewValidationError(
        fmt.Sprintf("field %q must be %s, got %s", field, expected, actual),
        []*tools.FieldIssue{
            {
                Field:            field,
                Constraint:       "invalid_field_type",
                ExpectedJSONType: expected,
                ActualJSONType:   actual,
            },
        },
        descriptions,
    )
}

func unknownJSONFieldError(path, field string, allowed []string) error {
    issueField := generatedJSONChildPath(path, field, false)
    return tools.NewValidationError(
        fmt.Sprintf("unknown field %q", issueField),
        []*tools.FieldIssue{
            {
                Field:      issueField,
                Constraint: "unknown_field",
                Allowed:    append([]string(nil), allowed...),
            },
        },
        nil,
    )
}

{{- if .Helpers }}
// Helper transform functions
{{- range .Helpers }}
func {{ .Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
{{ .Code }}
    return res
}

{{- end }}
{{- end }}
