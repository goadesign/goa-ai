// Used toolset typed helpers for {{ .Toolset.Name }}
// These helpers mirror the agent-as-tool helpers to provide a consistent planner UX.
// They expose typed payload/result aliases and `New<Tool>Call` builders.

// Tool IDs (globally unique). Use these constants in planner tool calls.
const (
{{- range .Toolset.Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .QualifiedName }}
{{- end }}
)

// Type aliases and codec re-exports for convenience.
{{- range .Toolset.Tools }}
type {{ goify .Name true }}Payload = {{ $.Toolset.SpecsPackageName }}specs.{{ goify .Name true }}Payload
var {{ goify .Name true }}PayloadCodec = {{ $.Toolset.SpecsPackageName }}specs.{{ goify .Name true }}PayloadCodec
{{- if .HasResult }}
type {{ goify .Name true }}Result  = {{ $.Toolset.SpecsPackageName }}specs.{{ goify .Name true }}Result
var {{ goify .Name true }}ResultCodec  = {{ $.Toolset.SpecsPackageName }}specs.{{ goify .Name true }}ResultCodec
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
{{- range .Toolset.Tools }}
// New{{ goify .Name true }}Call builds a planner.ToolRequest for {{ .QualifiedName }}.
func New{{ goify .Name true }}Call(args *{{ goify .Name true }}Payload, opts ...CallOption) planner.ToolRequest {
    req := planner.ToolRequest{
        Name:    {{ .ConstName }},
        Payload: args,
    }
    for _, o := range opts {
        if o != nil {
            o(&req)
        }
    }
    return req
}
{{- end }}


