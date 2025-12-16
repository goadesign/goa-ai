// Used toolset typed helpers for {{ .Toolset.Name }}
// These helpers mirror the agent-as-tool helpers to provide a consistent planner UX.
// They expose typed payload/result aliases and `New<Tool>Call` builders.

// Tool IDs (globally unique). Use these constants in planner tool calls.
const (
{{- range .Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
{{- end }}
)

// Type aliases and codec re-exports for convenience.
{{- range .Tools }}
type {{ .GoName }}Payload = {{ $.Toolset.SpecsPackageName }}specs.{{ .Payload.TypeName }}
var {{ .GoName }}PayloadCodec = {{ $.Toolset.SpecsPackageName }}specs.{{ .Payload.ExportedCodec }}
{{- if .Result }}
type {{ .GoName }}Result  = {{ $.Toolset.SpecsPackageName }}specs.{{ .Result.TypeName }}
var {{ .GoName }}ResultCodec  = {{ $.Toolset.SpecsPackageName }}specs.{{ .Result.ExportedCodec }}
{{- end }}
{{- end }}

// CallOption customizes planner.ToolRequest values built by the typed helpers.
type CallOption func(*planner.ToolRequest)

// WithParentToolCallID sets the ParentToolCallID on the constructed request.
func WithParentToolCallID(id string) CallOption {
    return func(r *planner.ToolRequest) { r.ParentToolCallID = id }
}

// WithToolCallID sets a model/tool-call identifier on the request. The runtime
// preserves this ID and echoes it in ToolResult.ToolCallID for correlation.
func WithToolCallID(id string) CallOption {
    return func(r *planner.ToolRequest) { r.ToolCallID = id }
}

// Typed tool-call helpers (one per tool). These ensure use of the generated tool ID
// and accept typed payloads matching tool schemas.
{{- range .Tools }}
// New{{ .GoName }}Call builds a planner.ToolRequest for {{ .Name }}.
func New{{ .GoName }}Call(args *{{ .GoName }}Payload, opts ...CallOption) planner.ToolRequest {
    var payload []byte
    if args != nil {
        // Encode typed payloads into canonical JSON using the generated codec.
        b, err := {{ .GoName }}PayloadCodec.ToJSON(args)
        if err != nil {
            panic(err)
        }
        payload = b
    }
    req := planner.ToolRequest{
        Name:    {{ .ConstName }},
        Payload: payload,
    }
    for _, o := range opts {
        if o != nil {
            o(&req)
        }
    }
    return req
}
{{- end }}


