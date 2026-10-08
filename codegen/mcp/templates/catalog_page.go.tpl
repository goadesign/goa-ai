{{- define "catalog-page" }}
body := &{{ .PayloadTransportRef }}{}
if p.Cursor != nil {
    cursor := {{ .Cursor.ValueTypeRef }}(*p.Cursor)
    body.{{ .Cursor.Selector }} = {{ if .Cursor.Pointer }}&{{ end }}cursor
}
payload, err := {{ .PayloadConstructor }}(body)
if err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
    return nil, goa.PermanentError("invalid_params", "%s", err.Error())
}
{{- if or .Endpoint.Credentials .Endpoint.Paths }}
if err := fill{{ .Endpoint.CallName }}Inputs(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources $.Operation }}{{ end }}{{ range .Endpoint.Paths }}, p.{{ index .Sources $.Operation }}{{ end }}); err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
    return nil, err
}
{{- end }}
result, err := a.{{ .Endpoint.CallName }}(ctx, payload)
if err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
    return nil, a.mapError(err, {{ if .Endpoint.FaultNames }}is{{ .Endpoint.CallName }}Fault{{ else }}isEndpointFault{{ end }}(err)).err
}
if err := {{ .Endpoint.Codec.ResultValidate }}(result); err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
    return nil, goa.PermanentError("internal_error", "%s", err.Error())
}
{{- if .NextCursor }}
{{ .NextCursor }}
{{- else }}
var nextCursor *string
{{- end }}
{{- end }}

{{- define "catalog-helpers" }}
{{- range .Helpers }}
// {{ .Declaration.Name }} copies a validated native descriptor value into its
// generated MCP representation, preserving the authored fields and types.
func {{ .Declaration.Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
    {{ .Code }}
    return res
}
{{- end }}
{{- end }}

{{- define "catalog-entry-checks" }}
{{- if .Metadata }}
{{- $catalog := . }}
{{- with .Metadata }}
metadataValue := {{ $catalog.Endpoint.ResultValue }}.{{ $catalog.EntriesField }}[index].{{ .Field }}
{{ if .Optional }}if metadataValue != nil { {{ end }}
    encodedMetadata, err := {{ .Encode }}(metadataValue)
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return nil, goa.PermanentError("internal_error", "%s", err.Error())
    }
    entry.{{ .TargetField }} = encodedMetadata
{{ if .Optional }} } {{ end }}
{{- end }}
{{- end }}
{{- if .CheckMeta }}
if err := validateContentMeta(entry.Meta); err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
    return nil, goa.PermanentError("internal_error", "%s", err.Error())
}
{{- end }}
{{- if .CheckSize }}
if entry.Size != nil && (math.IsNaN(*entry.Size) || math.IsInf(*entry.Size, 0)) {
    failure := goa.PermanentError("internal_error", "resource size must be finite")
    span.RecordError(failure)
    span.SetStatus(codes.Error, failure.Error())
    return nil, failure
}
{{- end }}
{{- if .CheckPriority }}
if entry.Annotations != nil && entry.Annotations.Priority != nil && (math.IsNaN(*entry.Annotations.Priority) || math.IsInf(*entry.Annotations.Priority, 0)) {
    failure := goa.PermanentError("internal_error", "resource priority must be finite")
    span.RecordError(failure)
    span.SetStatus(codes.Error, failure.Error())
    return nil, failure
}
{{- end }}
{{- end }}
