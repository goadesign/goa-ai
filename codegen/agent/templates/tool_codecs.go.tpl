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

{{- /* Compute whether any type has transport validation to gate helper emission */ -}}
{{- $hasValidation := false }}
{{- range .Types }}
    {{- if .TransportValidationSrc }}
        {{- $hasValidation = true }}
    {{- end }}
{{- end }}

// ValidationError wraps a validation failure and exposes issues that callers
// can use to build retry hints. It implements error and an Issues() accessor.
type ValidationError struct {
    msg          string
    issues       []*tools.FieldIssue
    descriptions map[string]string
}

func (e ValidationError) Error() string {
    return e.msg
}
func (e ValidationError) Issues() []*tools.FieldIssue {
    if len(e.issues) == 0 {
        return nil
    }
    out := make([]*tools.FieldIssue, len(e.issues))
    copy(out, e.issues)
    return out
}
func (e ValidationError) Descriptions() map[string]string {
    if len(e.descriptions) == 0 {
        return nil
    }
    out := make(map[string]string, len(e.descriptions))
    for k, v := range e.descriptions {
        out[k] = v
    }
    return out
}
{{- if $hasValidation }}
// newValidationError converts a goa.ServiceError (possibly merged) into a
// ValidationError with structured FieldIssue entries. It trims any leading
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
        issues = append(issues, &tools.FieldIssue{Field: field, Constraint: h.Name})
    }
    if len(issues) == 0 {
        return err
    }
    return &ValidationError{
        msg:    err.Error(),
        issues: issues,
    }
}
{{- end }}

{{- /* Per-type enrichment attaching descriptions for any type with validation (payload or non-payload) */ -}}
{{- range .Types }}
{{- if and .FieldDescs .TransportValidationSrc (ne (index .TransportValidationSrc 0) "") }}
func enrich{{ .TypeName }}ValidationError(err error) error {
    ve, ok := err.(*ValidationError)
    if !ok || ve == nil {
        return err
    }
    if len(ve.issues) == 0 {
        return err
    }
    m := make(map[string]string)
    {{- if .FieldDescs }}
    for _, is := range ve.issues {
        if d, ok := {{ .TypeName }}FieldDescs[is.Field]; ok && d != "" {
            m[is.Field] = d
        }
    }
    {{- end }}
    ve.descriptions = m
    return ve
}
{{- end }}
{{- end }}

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

// ServerDataCodec returns the generic codec for the named tool optional
// server-data payload when declared.
func ServerDataCodec(name string) (*tools.JSONCodec[any], bool) {
    switch name {
{{- range .Tools }}
    {{- if .OptionalServerData }}
    case {{ printf "%q" .Name }}:
        return &{{ .OptionalServerData.GenericCodec }}, true
    {{- end }}
{{- end }}
    default:
        return nil, false
    }
}

{{- range .Types }}
    {{- if .GenerateCodec }}
// {{ .MarshalFunc }} serializes {{ if .Pointer }}*{{ end }}{{ .FullRef }} into JSON.
func {{ .MarshalFunc }}(v {{ if .Pointer }}*{{ end }}{{ .FullRef }}) ([]byte, error) {
    {{- if .Pointer }}
    if v == nil {
        return nil, fmt.Errorf("{{ .NilError }}")
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
    if err := json.Unmarshal(data, &tv); err != nil {
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
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
    if err := json.Unmarshal(data, &v); err != nil {
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

{{- if .Helpers }}
// Helper transform functions
{{- range .Helpers }}
func {{ .Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
{{ .Code }}
    return res
}

{{- end }}
{{- end }}
