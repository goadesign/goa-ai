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

{{- /* Emit every static field fact in one structured representation. */ -}}
{{- range .Types }}
{{- if .Fields }}
var {{ .FieldsVar }} = []tools.FieldMetadata{
    {{- range .Fields }}
    {
        {{- if .Path }}
        Path: []tools.FieldPathSegment{
            {{- range .Path }}
            {{- if .Dynamic }}tools.DynamicField{},{{ else }}tools.FixedField({{ printf "%q" .Name }}),{{ end }}
            {{- end }}
        },
        {{- end }}
        {{- if .JSONType }}
        JSONType: {{ printf "%q" .JSONType }},
        {{- end }}
        {{- if .Description }}
        Description: {{ printf "%q" .Description }},
        {{- end }}
        {{- if .Branches }}
        Branches: []tools.UnionBranch{
            {{- range .Branches }}
            {
                Discriminator: []tools.FieldPathSegment{
                    {{- range .Discriminator }}
                    {{- if .Dynamic }}tools.DynamicField{},{{ else }}tools.FixedField({{ printf "%q" .Name }}),{{ end }}
                    {{- end }}
                },
                Value: {{ printf "%q" .Value }},
            },
            {{- end }}
        },
        {{- end }}
        {{- if .DiscriminatorValues }}
        DiscriminatorValues: []string{
            {{- range .DiscriminatorValues }}
            {{ printf "%q" . }},
            {{- end }}
        },
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
{{- if and .Fields .TransportValidationSrc (ne (index .TransportValidationSrc 0) "") }}
func {{ .EnrichValidationFunc }}(err error) error {
    var ve *tools.ValidationError
    if !errors.As(err, &ve) {
        return err
    }
    issues := ve.Issues()
    if len(issues) == 0 {
        return err
    }
    m := make(map[string]string)
    for _, is := range issues {
        if field, ok := tools.LookupFieldMetadata({{ .FieldsVar }}, is.Field); ok && field.Description != "" {
            m[is.Field] = field.Description
        }
    }
    return tools.NewValidationError(ve.Error(), issues, m)
}
{{- end }}
{{- end }}

{{- range .Types }}
{{- if .Fields }}
func {{ .InvalidFieldTypeFunc }}(err error) error {
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
    metadata, ok := tools.LookupFieldMetadata({{ .FieldsVar }}, field)
    if !ok || metadata.JSONType == "" {
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
                ExpectedJSONType: metadata.JSONType,
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
    if err := {{ .JSONValidatorFunc }}(data); err != nil {
        {{- if .Fields }}
        {{- if .Pointer }}
        return nil, {{ .InvalidFieldTypeFunc }}(err)
        {{- else }}
        return zero, {{ .InvalidFieldTypeFunc }}(err)
        {{- end }}
        {{- else }}
        {{- if .Pointer }}
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- else }}
        return zero, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- end }}
        {{- end }}
    }
    if err := json.Unmarshal(data, &tv); err != nil {
        {{- if .Fields }}
        {{- if .Pointer }}
        return nil, {{ .InvalidFieldTypeFunc }}(err)
        {{- else }}
        return zero, {{ .InvalidFieldTypeFunc }}(err)
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
    if err := toolhttp.{{ .ValidateFunc }}({{ if .TransportPointer }}&{{ end }}tv); err != nil {
        err = newValidationError(err)
        {{- if .Fields }}
        err = {{ .EnrichValidationFunc }}(err)
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
    if err := {{ .JSONValidatorFunc }}(data); err != nil {
        {{- if .Fields }}
        err = {{ .InvalidFieldTypeFunc }}(err)
        {{- end }}
        {{- if .Pointer }}
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- else }}
        return zero, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- end }}
    }
    if err := json.Unmarshal(data, &v); err != nil {
        {{- if .Fields }}
        err = {{ .InvalidFieldTypeFunc }}(err)
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

{{- range .JSONDocumentValidators }}
// {{ .Name }} parses one JSON document and checks the exact value
// shapes known from this generated Goa type.
func {{ .Name }}(data []byte) error {
    var root any
    dec := json.NewDecoder(bytes.NewReader(data))
    dec.UseNumber()
    if err := dec.Decode(&root); err != nil {
        return err
    }
    if err := dec.Decode(&struct{}{}); err != io.EOF {
        return fmt.Errorf("multiple JSON documents")
    }
    return {{ .Root }}("", root, "")
}
{{- end }}

{{- range .JSONValidators }}

// {{ .Name }} checks one value whose JSON shape is fixed by the generated Goa type.
func {{ .Name }}(path string, value any, description string) error {
    {{- if eq .Kind "any" }}
    return nil
    {{- else }}
    field := path
    if field == "" {
        field = "$payload"
    }
    if value == nil {
        return invalidGeneratedFieldTypeError(field, {{ printf "%q" .Expected }}, "null", description)
    }
    {{- $usesTyped := or .SignedInteger .UnsignedInteger (eq .Kind "object") (eq .Kind "array") (eq .Kind "map") }}
    {{- if or (eq .Expected "integer") (eq .Expected "number") }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.(json.Number)
    {{- else if eq .Expected "string" }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.(string)
    {{- else if eq .Expected "boolean" }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.(bool)
    {{- else if eq .Expected "array" }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.([]any)
    {{- else if eq .Expected "object" }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.(map[string]any)
    {{- end }}
    if !ok {
        return invalidGeneratedFieldTypeError(field, {{ printf "%q" .Expected }}, decodedJSONType(value), description)
    }
    {{- if .SignedInteger }}
    if _, err := strconv.ParseInt(typed.String(), 10, {{ if .IntegerBits }}{{ .IntegerBits }}{{ else }}strconv.IntSize{{ end }}); err != nil {
        return invalidGeneratedFieldTypeError(field, "integer", "number", description)
    }
    {{- else if .UnsignedInteger }}
    if _, err := strconv.ParseUint(typed.String(), 10, {{ if .IntegerBits }}{{ .IntegerBits }}{{ else }}strconv.IntSize{{ end }}); err != nil {
        return invalidGeneratedFieldTypeError(field, "integer", "number", description)
    }
    {{- end }}
    {{- if eq .Kind "object" }}
    keys := make([]string, 0, len(typed))
    for key := range typed {
        keys = append(keys, key)
    }
    sort.Strings(keys)
    for _, key := range keys {
        switch key {
        {{- range .Fields }}
        case {{ printf "%q" .Name }}:
            {{- if .Call }}
            if err := {{ .Call.Name }}(
                generatedJSONChildPath(path, key, false),
                typed[key],
                {{- if .Call.InheritDescription }}description{{ else }}{{ printf "%q" .Call.Description }}{{ end }},
            ); err != nil {
                return err
            }
            {{- end }}
        {{- end }}
        default:
            return unknownJSONFieldError(path, key, []string{
                {{- range .Fields }}
                {{ printf "%q" .Name }},
                {{- end }}
            })
        }
    }
    {{- else if eq .Kind "array" }}
    for index, item := range typed {
        if err := {{ .Element.Name }}(
            generatedJSONChildPath(path, strconv.Itoa(index), true),
            item,
            {{- if .Element.InheritDescription }}description{{ else }}{{ printf "%q" .Element.Description }}{{ end }},
        ); err != nil {
            return err
        }
    }
    {{- else if eq .Kind "map" }}
    keys := make([]string, 0, len(typed))
    for key := range typed {
        keys = append(keys, key)
    }
    sort.Strings(keys)
    for _, key := range keys {
        if err := {{ .Element.Name }}(
            generatedJSONChildPath(path, key, true),
            typed[key],
            {{- if .Element.InheritDescription }}description{{ else }}{{ printf "%q" .Element.Description }}{{ end }},
        ); err != nil {
            return err
        }
    }
    {{- end }}
    return nil
    {{- end }}
}
{{- end }}

// generatedJSONChildPath keeps fixed object fields dotted. After an array index
// or caller-defined map key, it uses slash-separated paths so later keys stay clear.
func generatedJSONChildPath(path, key string, useSlashPath bool) string {
    if useSlashPath || strings.HasPrefix(path, "/") {
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
