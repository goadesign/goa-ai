{{ printf "%s creates HTTP clients for all the %s service servers." .Transport.ClientInitDeclaration.Name .Transport.Service.Name | comment }}
func {{ .Transport.ClientInitDeclaration.Name }}(
	scheme string,
	host string,
	doer goahttp.Doer,
	enc func(*http.Request) goahttp.Encoder,
	dec func(*http.Response) goahttp.Decoder,
	restoreBody bool,
) *{{ .Transport.ClientStructDeclaration.Name }} {
	doer = mcpruntime.NewHTTPTransport(doer, mcpruntime.ClientInfo{}, map[string]mcpruntime.ToolBinding{
        {{- range .Tools }}
        {{- if or .Headers .ReadOnly .Idempotent }}
        {{ printf "%q" .Name }}: {
            {{- if .ReadOnly }}ReadOnly: true,{{ end }}
            {{- if .Idempotent }}Idempotent: true,{{ end }}
            {{- if .Headers }}
            Headers: []mcpruntime.HeaderBinding{
            {{- range .Headers }}
            {Name: {{ printf "%q" .Name }}, Type: {{ printf "%q" .Type }}, Path: []string{ {{ range .Path }}{{ printf "%q" . }}, {{ end }} }},
            {{- end }}
            },
            {{- end }}
        },
        {{- end }}
        {{- end }}
    }, mcpruntime.InputSupport{}, mcpruntime.HTTPRetryPolicy{})
	return &{{ .Transport.ClientStructDeclaration.Name }}{
		Doer:                doer,
		{{- range .Transport.Endpoints }}
		{{- if isSSEEndpoint . }}
		{{ .Method.VarName }}Doer: doer,
		{{- end }}
		{{- end }}
		RestoreResponseBody: restoreBody,
		scheme:              scheme,
		host:                host,
		decoder:             dec,
		encoder:             enc,
	}
}
