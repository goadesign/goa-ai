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
