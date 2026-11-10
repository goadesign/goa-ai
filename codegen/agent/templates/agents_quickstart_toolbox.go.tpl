---
<details>
<summary><strong>Click to see a detailed reference of your agent's toolboxes...</strong></summary>

## 6. Your Agent's Toolbox: A Reference

{{- range .Services }}
{{- range .Agents }}

### Agent `{{ .Name }}` Toolsets

* **Tools this agent can USE:**
{{- if .UsedToolsets }}
{{- range .UsedToolsets }}
* **`{{ .QualifiedName }}`** {{ if .ProviderLabel }}(MCP Suite: `{{ .ProviderLabel }}`){{ end }}
{{- if .Tools }}
{{- range .Tools }}
* **Tool: `{{ .QualifiedName }}`**
* *{{ .Description }}*
{{- if .IsMethodBacked }}
  * Bound to service method `{{ .MethodGoName }}`; its validation and errors apply.
{{- end }}
{{- if .InjectedFields }}
  * Server-injected (never ask the model for these): {{ range $i, $f := .InjectedFields }}{{ if $i }}, {{ end }}`{{ $f }}`{{ end }}
{{- end }}
{{- if .Bounds }}
  * Bounded result: the service reports `returned` and `truncated`{{ with .Bounds.Paging }}{{ if .ContinueTool }}; continue with `{{ .ContinueTool }}`{{ end }}{{ end }}
{{- end }}
{{- if .Confirmation }}
  * Requires confirmation before it runs
{{- end }}
{{- if .ServerData }}
  * Server data: {{ range $i, $d := .ServerData }}{{ if $i }}, {{ end }}`{{ $d.Kind }}`{{ if $d.NativeImage }} (an image the model sees){{ end }}{{ end }}
{{- end }}
{{- if .TerminalRun }}
  * Ends the run when it succeeds
{{- end }}
{{- if .Bookkeeping }}
  * Bookkeeping: does not consume the run's retrieval budget
{{- end }}
{{- if .RequiresUI }}
  * Excluded from text-only runs: running it requires a UI
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- else }}
* *This agent does not use any toolsets.*
{{- end }}
* Exact input schemas for this agent, including `BindField` and lookup variants: `{{ .Dir }}/specs/tool_schemas.json` (`input_binding`).
* **Tools this agent EXPORTS for others to use:**
{{- if .ExportedToolsets }}
{{- range .ExportedToolsets }}
* **`{{ .QualifiedName }}`**
{{- end }}
{{- else }}
* *This agent does not export any toolsets.*
{{- end }}
{{- end }}
{{- end }}
</details>

