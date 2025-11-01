// {{ $.Toolset.Name }} adapter stub for {{ $.Agent.StructName }}
//
// This file declares a minimal adapter implementation for the method-backed
// toolset {{ $.Toolset.Name }}. The adapter maps tool payloads and results to
// the bound service method types. Replace the "not implemented" placeholders
// with concrete field mappings. Keep business logic out of main; import this
// package from your service main when wiring agents.

import (
    "context"
    "errors"
)

// {{ goify $.Agent.GoName true }}{{ goify $.Toolset.PathName true }}Adapter implements the generated
// {{ $.ToolsetAlias }}.{{ goify $.Toolset.Name true }}Adapter interface. All methods return
// a clear not-implemented error by default to make the build succeed while
// prompting developers to provide proper mappings.
type {{ goify $.Agent.GoName true }}{{ goify $.Toolset.PathName true }}Adapter struct{}

// New{{ goify $.Agent.GoName true }}{{ goify $.Toolset.PathName true }}Adapter constructs the stub adapter.
func New{{ goify $.Agent.GoName true }}{{ goify $.Toolset.PathName true }}Adapter() *{{ goify $.Agent.GoName true }}{{ goify $.Toolset.PathName true }}Adapter {
    return &{{ goify $.Agent.GoName true }}{{ goify $.Toolset.PathName true }}Adapter{}
}

{{- range .Toolset.Tools }}
{{- if .IsMethodBacked }}
// {{ goify .Name true }} maps tool payload to the bound service method payload.
func (a *{{ goify $.Agent.GoName true }}{{ goify $.Toolset.PathName true }}Adapter) {{ goify .Name true }}(ctx context.Context, meta {{ $.ToolsetAlias }}.{{ goify $.Toolset.Name true }}ToolMeta, args *{{ $.SpecsAlias }}.{{ printf "%sPayload" (goify .Name true) }}) ({{ if .MethodPayloadTypeRef }}{{ .MethodPayloadTypeRef }}{{ else }}*{{ $.Toolset.SourceService.PkgName }}.{{ if .MethodPayloadTypeName }}{{ .MethodPayloadTypeName }}{{ else }}{{ .MethodGoName }}Payload{{ end }}{{ end }}, error) {
    return nil, errors.New("{{ $.Toolset.Name }}.{{ .Name }} adapter not implemented")
}

{{- if .HasResult }}
// {{ goify .Name true }}Result maps the service method result to the tool result.
func (a *{{ goify $.Agent.GoName true }}{{ goify $.Toolset.PathName true }}Adapter) {{ goify .Name true }}Result(ctx context.Context, meta {{ $.ToolsetAlias }}.{{ goify $.Toolset.Name true }}ToolMeta, res {{ if .MethodResultTypeRef }}{{ .MethodResultTypeRef }}{{ else }}*{{ $.Toolset.SourceService.PkgName }}.{{ if .MethodResultTypeName }}{{ .MethodResultTypeName }}{{ else }}{{ .MethodGoName }}Result{{ end }}{{ end }}) (*{{ $.SpecsAlias }}.{{ printf "%sResult" (goify .Name true) }}, error) {
    return nil, errors.New("{{ $.Toolset.Name }}.{{ .Name }} result adapter not implemented")
}
{{- end }}
{{- end }}
{{- end }}
