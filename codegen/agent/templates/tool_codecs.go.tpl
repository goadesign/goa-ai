var (
{{- $printed := false }}
{{- range .Types }}
    {{- if .GenerateCodec }}
    {{- if $printed }}

    {{- end }}
    // {{ .ExportedCodec }} serializes values of type {{ if .Pointer }}*{{ end }}{{ .FullRef }} to canonical JSON.
    {{ .ExportedCodec }} = tools.JSONCodec[{{ if .Pointer }}*{{ end }}{{ .FullRef }}]{
        ToJSON:   {{ .MarshalFunc }},
        FromJSON: {{ .UnmarshalFunc }},
    }
    {{- $printed = true }}
    {{- end }}
{{- end }}
{{- $printed = false }}
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
var {{ .TypeName }}FieldDescs = map[string]string{
    {{- range $k, $v := .FieldDescs }}
    {{ printf "%q" $k }}: {{ printf "%q" $v }},
    {{- end }}
}
{{- end }}
{{- end }}

{{- /* Emit generated JSON type metadata per type if available */ -}}
{{- range .Types }}
{{- if .FieldJSONTypes }}
var {{ .TypeName }}FieldJSONTypes = map[string]string{
    {{- range $k, $v := .FieldJSONTypes }}
    {{ printf "%q" $k }}: {{ printf "%q" $v }},
    {{- end }}
}
{{- end }}
{{- end }}

{{- /* Emit generated closed-object key metadata per type if available */ -}}
{{- range .Types }}
{{- if .FieldAllowedObjectKeys }}
var {{ .TypeName }}FieldAllowedObjectKeys = map[string][]string{
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
        if d, ok := {{ .TypeName }}FieldDescs[is.Field]; ok && d != "" {
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
    expected, ok := {{ .TypeName }}FieldJSONTypes[field]
    if !ok {
        return err
    }
    actual := typeErr.Value
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

{{- if .EmitToolLookups }}
// PayloadCodec returns the generic codec for the named tool payload.
func PayloadCodec(name string) (*tools.JSONCodec[any], bool) {
    switch name {
{{- range .Tools }}
    {{- if .Payload }}
    case {{ printf "%q" .Name }}:
        return &{{ .Payload.GenericCodec }}, true
    {{- end }}
{{- end }}
    default:
        return nil, false
    }
}

// ResultCodec returns the generic codec for the named tool result.
func ResultCodec(name string) (*tools.JSONCodec[any], bool) {
    switch name {
{{- range .Tools }}
    {{- if .Result }}
    case {{ printf "%q" .Name }}:
        return &{{ .Result.GenericCodec }}, true
    {{- end }}
{{- end }}
    default:
        return nil, false
    }
}
{{- end }}

{{- range .Types }}
    {{- if .GenerateCodec }}
// {{ .MarshalFunc }} serializes {{ if .Pointer }}*{{ end }}{{ .FullRef }} into JSON.
func {{ .MarshalFunc }}(v {{ if .Pointer }}*{{ end }}{{ .FullRef }}) ([]byte, error) {
    {{- if .Pointer }}
    if v == nil {
        {{- if eq .Usage "sidecar" }}
        return []byte("null"), nil
        {{- else }}
        return nil, fmt.Errorf("{{ .NilError }}")
        {{- end }}
    }
    {{- end }}
    {{- if and .TransportTypeName .Pointer }}
    in := v
    _ = in
    var out *toolhttp.{{ .TransportTypeName }}
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
    {{- if and .TransportTypeName .Pointer }}
    var tv toolhttp.{{ .TransportTypeName }}
    {{- if .FieldAllowedObjectKeys }}
    if err := decodeKnownJSON(data, &tv, {{ .TypeName }}FieldAllowedObjectKeys); err != nil {
    {{- else if eq .Usage "payload" }}
    if err := decodeStrictJSON(data, &tv); err != nil {
    {{- else }}
    if err := json.Unmarshal(data, &tv); err != nil {
    {{- end }}
        {{- if .FieldJSONTypes }}
        return nil, invalid{{ .TypeName }}FieldTypeError(err)
        {{- else }}
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- end }}
    }
    {{- if .TransportValidationSrc }}
    if err := toolhttp.Validate{{ .TransportTypeName }}(&tv); err != nil {
        err = newValidationError(err)
        {{- if .FieldDescs }}
        err = enrich{{ .TypeName }}ValidationError(err)
        {{- end }}
        return nil, err
    }
    {{- end }}
    in := &tv
    _ = in
    var out *{{ .FullRef }}
{{ .DecodeTransform }}
    return out, nil
    {{- else }}
    var v {{ .FullRef }}
    {{- if .FieldAllowedObjectKeys }}
    if err := decodeKnownJSON(data, &v, {{ .TypeName }}FieldAllowedObjectKeys); err != nil {
    {{- else if eq .Usage "payload" }}
    if err := decodeStrictJSON(data, &v); err != nil {
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

// decodeStrictJSON decodes one JSON document and rejects object fields that are
// not present in the generated transport contract.
func decodeStrictJSON(data []byte, v any) error {
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

// decodeKnownJSON decodes one JSON document after rejecting fields outside the
// generated closed-object payload/result contracts. It preserves open map/object
// fields by only validating paths present in allowed.
func decodeKnownJSON(data []byte, v any, allowed map[string][]string) error {
    if err := validateKnownJSONFields(data, allowed); err != nil {
        return err
    }
    return json.Unmarshal(data, v)
}

func validateKnownJSONFields(data []byte, allowed map[string][]string) error {
    var root any
    dec := json.NewDecoder(bytes.NewReader(data))
    if err := dec.Decode(&root); err != nil {
        return err
    }
    if err := dec.Decode(&struct{}{}); err != io.EOF {
        return fmt.Errorf("multiple JSON documents")
    }
    return validateKnownJSONValue("", root, allowed)
}

func validateKnownJSONValue(path string, value any, allowed map[string][]string) error {
    switch v := value.(type) {
    case []any:
        for _, item := range v {
            if err := validateKnownJSONValue(path, item, allowed); err != nil {
                return err
            }
        }
        return nil
    case map[string]any:
        allowedKeys, ok := allowed[path]
        if !ok {
            return nil
        }
        keys := make([]string, 0, len(v))
        for key := range v {
            keys = append(keys, key)
        }
        sort.Strings(keys)
        for _, key := range keys {
            if !slices.Contains(allowedKeys, key) {
                return unknownJSONFieldError(path, key, allowedKeys)
            }
            childPath := key
            if path != "" {
                childPath = path + "." + key
            }
            if err := validateKnownJSONValue(childPath, v[key], allowed); err != nil {
                return err
            }
        }
    }
    return nil
}

func unknownJSONFieldError(path, field string, allowed []string) error {
    issueField := field
    if path != "" {
        issueField = path + "." + field
    }
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
