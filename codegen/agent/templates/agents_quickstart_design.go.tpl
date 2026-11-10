---

## 1. Your Design, At a Glance ✨

Here’s a map of what Goa-AI just built for you based on your `design/*.go` files.

{{- range .Services }}
* **Service `{{ .Service.Name }}`:**
{{- if .Agents }}
{{- range .Agents }}
    * **Agent `{{ .Name }}`** (ID: `{{ .ID }}`):
        * **Mission:** *{{ .Description }}*
        * **Uses Toolsets:**
            {{- if .UsedToolsets }}
            {{- range .UsedToolsets }}
            * `{{ .QualifiedName }}`{{ if .ProviderLabel }} (from remote MCP service `{{ .ProviderLabel }}`){{ end }}
            {{- end }}
            {{- else }}*none*
            {{- end }}
        * **Exports Toolsets:**
            {{- if .ExportedToolsets }}
            {{- range .ExportedToolsets }}
            * `{{ .QualifiedName }}`
            {{- end }}
            {{- else }}*none*
            {{- end }}
        * **Run Policy:**
            * Max Tool Calls: `{{ .RunPolicy.Caps.MaxToolCalls }}`
            * Max Recovery Turns: `{{ .RunPolicy.Caps.EffectiveMaxRecoveryTurns }}`
            * Time Budget: `{{ .RunPolicy.TimeBudget }}`
{{- end }}
    * **Direct Completions:**
        {{- if .Completions }}
        {{- range .Completions }}
        * `{{ .Name }}`
        {{- end }}
        {{- else }}*none*
        {{- end }}
{{- else }}
* This service doesn't define any agents itself, but it might provide tools for others to use!
{{- end }}
{{- end }}

