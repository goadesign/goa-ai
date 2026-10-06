// mcpHTTPBindings supplies the HTTP inputs declared by this Goa service.
// Each constructor receives independent tool facts and credential query names.
func mcpHTTPBindings() mcpruntime.HTTPBindings {
    return mcpruntime.HTTPBindings{
        {{- if .Tools }}
        Tools: map[string]mcpruntime.ToolBinding{
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
        },
        {{- end }}
        {{- if .CredentialQueries }}
        CredentialQueries: map[string][]string{
            {{- range $method, $names := .CredentialQueries }}
            {{ printf "%q" $method }}: { {{ range $names }}{{ printf "%q" . }}, {{ end }} },
            {{- end }}
        },
        {{- end }}
    }
}
