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
            // Fallback: marshal structurally compatible values directly.
            return json.Marshal(v)
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

{{- /* Compute whether any type has validation to gate helper emission */ -}}
{{- $hasValidation := false }}
{{- range .Types }}
    {{- if or .Validation .JSONValidation }}
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
// newValidationError converts a goa.ServiceError (possibly merged) into a
// ValidationError with structured FieldIssue entries. It trims any leading
// "body." from field names for conciseness.
{{- if $hasValidation }}
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
{{- if and (or .Validation .JSONValidation) .NeedType }}
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

{{- range .Types }}
    {{- if .GenerateCodec }}
// {{ .MarshalFunc }} serializes {{ if .Pointer }}*{{ end }}{{ .FullRef }} into JSON.
func {{ .MarshalFunc }}(v {{ if .Pointer }}*{{ end }}{{ .FullRef }}) ([]byte, error) {
    {{- if .Pointer }}
    if v == nil {
        return nil, fmt.Errorf("{{ .NilError }}")
    }
    {{- end }}
    return json.Marshal(v)
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
    {{- if eq .Usage "payload" }}
    // Decode into JSON body (server body style) then transform.
    // Note: Agent used-tools perform lenient decode (no required-field validation here).
    var raw {{ .JSONRef }}
    if err := json.Unmarshal(data, &raw); err != nil {
        {{- if .Pointer }}
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- else }}
        return zero, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- end }}
    }
    {{- if .JSONValidationSrc }}
    var err error
    {{- range .JSONValidationSrc }}
    {{ . }}
    {{- end }}
    if err != nil {
        err = newValidationError(err)
        {{- if and (or .Validation .JSONValidation) .NeedType }}
        {{- if .Pointer }}
        return nil, err
        {{- else }}
        return zero, err
        {{- end }}
        {{- else }}
        {{- if .Pointer }}
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- else }}
        return zero, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- end }}
        {{- end }}
    }
    {{- end }}
    // Transform into final type
    {{- if .TransformBody }}
    {{ .TransformBody }}
    {{- else }}
    // Fallback: direct assignment if shapes are identical
    v := {{ if .Pointer }}*{{ end }}{{ .FullRef }}(raw)
    {{- end }}
    {{- if .Pointer }}
    return v, nil
    {{- else }}
    return *v, nil
    {{- end }}
    {{- else }}
    // Non-payload types: simple decode
    var v {{ if .Pointer }}*{{ end }}{{ .FullRef }}
    if err := json.Unmarshal(data, &v); err != nil {
        {{- if .Pointer }}
        return nil, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- else }}
        return zero, fmt.Errorf("{{ .DecodeError }}: %w", err)
        {{- end }}
    }
    return v, nil
    {{- end }}
}
    {{- end }}
{{- end }}

// Transform helpers
{{- range .Types }}
    {{- range .TransformHelpers }}
func {{ .Name }}(v {{ .ParamTypeRef }}) (out {{ .ResultTypeRef }}) {
{{ .Code }}
    out = res
    return
}
    {{- end }}
{{- end }}

{{- /* Emit standalone validators for embedded user types that require them. */ -}}
{{- range .Types }}
    {{- if and (not .GenerateCodec) .ValidateFunc }}

// {{ .ValidateFunc }} validates values of type {{ .FullRef }}.
func {{ .ValidateFunc }}(body {{ .FullRef }}) (err error) {
    {{- if .ValidationSrc }}
        {{- range .ValidationSrc }}
    {{ . }}
        {{- end }}
    {{- end }}
    return
}
    {{- end }}
{{- end }}
