// {{ .Toolset.Name }}ServiceToolset wires method-backed tools to the service implementation
// using generated adapters (JSON-based by default) and optional user-provided overrides.
// The Execute function dispatches each tool to the corresponding service method.
package {{ .PackageName }}

import (
    "context"
    "errors"

    "goa.design/goa-ai/agents/runtime/policy"
    "goa.design/goa-ai/agents/runtime/planner"
    "goa.design/goa-ai/agents/runtime/runtime"
    "goa.design/goa-ai/agents/runtime/tools"
)

// {{ .Toolset.Name | goify true }}Config configures method-backed tool dispatch for the service.
// Service is required. Each Adapter is REQUIRED and maps tool args to the
// service payload. Each ResultAdapter is REQUIRED and maps the service result
// back to the tool result type.
//
// Example:
//  cfg := {{ .Toolset.Name | goify true }}Config{
//      Service: svc,
//      {{- range .Toolset.Tools }}
//      {{- if .IsMethodBacked }}
//      {{ .MethodGoName }}Adapter: func(ctx context.Context, args *{{ $.Agent.ToolSpecsPackage }}.{{ printf "%s%sPayload" (goify $.Toolset.Name) (goify .Name) }}) ({{ $.Agent.Service.PkgName }}.{{ .MethodGoName }}Payload, error) { /* map */ },
//      {{ .MethodGoName }}ResultAdapter: func(ctx context.Context, res *{{ $.Agent.Service.PkgName }}.{{ .MethodGoName }}Result) (*{{ $.Agent.ToolSpecsPackage }}.{{ printf "%s%sResult" (goify $.Toolset.Name) (goify .Name) }}, error) { /* map */ },
//      {{- end }}
//      {{- end }}
//  }
// {{ .Toolset.Name | goify true }}Client narrows dependencies to the bound methods.
// It is satisfied by the generated Goa client for the target service.
type {{ .Toolset.Name | goify true }}Client interface {
{{- range .Toolset.Tools }}{{- if .IsMethodBacked }}
    {{ .MethodGoName }}(ctx context.Context, p *{{ $.Agent.Service.PkgName }}.{{ .MethodGoName }}Payload) (*{{ $.Agent.Service.PkgName }}.{{ .MethodGoName }}Result, error)
{{- end }}{{- end }}
}

type {{ .Toolset.Name | goify true }}Config struct {
    Client {{ .Toolset.Name | goify true }}Client
{{- range .Toolset.Tools }}
{{- if .IsMethodBacked }}
    {{ .MethodGoName }}Adapter func(ctx context.Context, args *{{ $.Agent.ToolSpecsPackage }}.{{ printf "%s%sPayload" (goify $.Toolset.Name) (goify .Name) }}) ({{ $.Agent.Service.PkgName }}.{{ .MethodGoName }}Payload, error)
    {{ .MethodGoName }}ResultAdapter func(ctx context.Context, res *{{ $.Agent.Service.PkgName }}.{{ .MethodGoName }}Result) (*{{ $.Agent.ToolSpecsPackage }}.{{ printf "%s%sResult" (goify $.Toolset.Name) (goify .Name) }}, error)
{{- end }}
{{- end }}
}

// New{{ .Agent.GoName }}{{ .Toolset.PathName | goify true }}ServiceToolsetRegistration returns a ToolsetRegistration
// that dispatches method-backed tools to the service via mandatory adapters.
func New{{ .Agent.GoName }}{{ .Toolset.PathName | goify true }}ServiceToolsetRegistration(cfg {{ .Toolset.Name | goify true }}Config) runtime.ToolsetRegistration {
    return runtime.ToolsetRegistration{
        Name:        {{ printf "%q" .Toolset.QualifiedName }},
        Description: {{ printf "%q" .Toolset.Description }},
        Specs:       {{ $.Agent.ToolSpecsPackage }}.Specs,
        Metadata:    policy.ToolMetadata{ID: {{ printf "%q" .Toolset.Name }}, Name: {{ printf "%q" .Toolset.Name }}},
        Execute: func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
            if cfg.Client == nil {
                return planner.ToolResult{Error: errors.New("client is required")}, nil
            }
            switch call.Name {
            {{- range .Toolset.Tools }}
            {{- if .IsMethodBacked }}
            case {{ printf "%q" .QualifiedName }}:
                var argsPtr *{{ $.Agent.ToolSpecsPackage }}.{{ printf "%s%sPayload" (goify $.Toolset.Name) (goify .Name) }}
                switch v := call.Payload.(type) {
                case *{{ $.Agent.ToolSpecsPackage }}.{{ printf "%s%sPayload" (goify $.Toolset.Name) (goify .Name) }}:
                    argsPtr = v
                case nil:
                    // leave nil; adapter may supply defaults
                default:
                    return planner.ToolResult{Error: errors.New("invalid payload type for tool: " + call.Name)}, nil
                }
                if cfg.{{ .MethodGoName }}Adapter == nil {
                    return planner.ToolResult{Error: errors.New("adapter required for method-backed tool: " + call.Name)}, nil
                }
                req, err := cfg.{{ .MethodGoName }}Adapter(ctx, argsPtr)
                if err != nil { return planner.ToolResult{Error: err}, nil }
                res, err := cfg.Client.{{ .MethodGoName }}(ctx, &req)
                if err != nil { return planner.ToolResult{Error: err}, nil }
                if cfg.{{ .MethodGoName }}ResultAdapter == nil {
                    return planner.ToolResult{Error: errors.New("result adapter required for method-backed tool: " + call.Name)}, nil
                }
                out, rerr := cfg.{{ .MethodGoName }}ResultAdapter(ctx, res)
                if rerr != nil { return planner.ToolResult{Error: rerr}, nil }
                return planner.ToolResult{Payload: out}, nil
            {{- end }}
            {{- end }}
            default:
                return planner.ToolResult{Error: errors.New("tool not found in service toolset: " + call.Name)}, nil
            }
        },
    }
}


