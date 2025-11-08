// Default service executor for {{ .Toolset.Name }}
// This factory builds a runtime.ToolCallExecutor that dispatches tool calls to
// user-provided per-tool callers. It decodes tool payloads with generated codecs,
// allows optional payload/result mappers, and returns results as-is (or mapped).
//
// The executor is generic and does not wire a client automatically. Supply a
// caller for each tool using the generated With<Tool> option. Provide mapping
// functions when tool payload/result shapes differ from method payload/result.
//
// Example:
//
//   exec := {{ .Toolset.PackageName }}.New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec(
//       {{- range .Toolset.Tools }}
//       {{- if .IsMethodBacked }}
//       {{ $.Toolset.PackageName }}.With{{ goify .Name true }}(func(ctx context.Context, in any) (any, error) {
//           // call service client with 'in' (method payload), return method result
//           return nil, nil
//       }),
//       {{- end }}
//       {{- end }}
//       {{ .Toolset.PackageName }}.WithPayloadMapper(func(id tools.Ident, in any, meta runtime.ToolCallMeta) (any, error) {
//           // map tool payload -> method payload by id
//           return in, nil
//       }),
//       {{ .Toolset.PackageName }}.WithResultMapper(func(id tools.Ident, out any, meta runtime.ToolCallMeta) (any, error) {
//           // map method result -> tool result by id
//           return out, nil
//       }),
//   )
//
//   // Register:
//   // reg := {{ .Agent.GoName }}{{ goify .Toolset.PathName true }}.New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}ToolsetRegistration(exec)
//   // rt.RegisterToolset(reg)

type (
    seCfg struct {
        callers map[tools.Ident]func(context.Context, any) (any, error)
        mapPayload func(tools.Ident, any, runtime.ToolCallMeta) (any, error)
        mapResult  func(tools.Ident, any, runtime.ToolCallMeta) (any, error)
    }
    // ExecOpt customizes the default service executor.
    ExecOpt interface{ apply(*seCfg) }
)

type execOptFunc func(*seCfg)
func (f execOptFunc) apply(c *seCfg) { f(c) }

// WithPayloadMapper installs a mapper for tool payload -> method payload.
func WithPayloadMapper(f func(tools.Ident, any, runtime.ToolCallMeta) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) { c.mapPayload = f })
}

// WithResultMapper installs a mapper for method result -> tool result.
func WithResultMapper(f func(tools.Ident, any, runtime.ToolCallMeta) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) { c.mapResult = f })
}

{{- range .Toolset.Tools }}
{{- if .IsMethodBacked }}
// With{{ goify .Name true }} sets the caller for {{ .QualifiedName }}.
func With{{ goify .Name true }}(f func(context.Context, any) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) {
        if c.callers == nil { c.callers = make(map[tools.Ident]func(context.Context, any) (any, error)) }
        c.callers[tools.Ident({{ printf "%q" .QualifiedName }})] = f
    })
}
{{- end }}
{{- end }}

// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec returns a ToolCallExecutor that
// decodes tool payloads with generated codecs, applies optional mappers, calls user-provided
// per-tool callers, and maps results back.
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec(opts ...ExecOpt) runtime.ToolCallExecutor {
    var cfg seCfg
    for _, o := range opts {
        if o != nil { o.apply(&cfg) }
    }
    // Preflight: ensure callers are provided for all method-backed tools.
    {
        var missing []string
        {{- range .Toolset.Tools }}
        {{- if .IsMethodBacked }}
        if cfg.callers == nil || cfg.callers[tools.Ident({{ printf "%q" .QualifiedName }})] == nil {
            missing = append(missing, {{ printf "%q" .QualifiedName }})
        }
        {{- end }}
        {{- end }}
        if len(missing) > 0 {
            panic(fmt.Errorf("service executor missing callers for tools: %s", strings.Join(missing, ", ")))
        }
    }
    return runtime.ToolCallExecutorFunc(func(ctx context.Context, meta runtime.ToolCallMeta, call planner.ToolRequest) (planner.ToolResult, error) {
        // Lookup caller
        caller := cfg.callers[call.Name]
        if caller == nil {
            return planner.ToolResult{
                Name:  call.Name,
                Error: planner.NewToolError("caller is required"),
            }, nil
        }
        // Decode tool payload if needed
        var toolArgs any
        switch v := call.Payload.(type) {
        case json.RawMessage:
            if pc, ok := {{ $.Toolset.SpecsPackageName }}.PayloadCodec(string(call.Name)); ok && pc != nil && pc.FromJSON != nil {
                val, err := pc.FromJSON(v)
                if err != nil {
                    return planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err)}, nil
                }
                toolArgs = val
            } else {
                toolArgs = v
            }
        default:
            toolArgs = v
        }
        // Map to method payload
        methodIn := toolArgs
        if cfg.mapPayload != nil {
            var err error
            methodIn, err = cfg.mapPayload(call.Name, toolArgs, meta)
            if err != nil {
                return planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err)}, nil
            }
        }
        // Invoke caller
        methodOut, err := caller(ctx, methodIn)
        if err != nil {
            return planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err)}, nil
        }
        // Map back to tool result
        result := methodOut
        if cfg.mapResult != nil {
            if val, e := cfg.mapResult(call.Name, methodOut, meta); e == nil {
                result = val
            } else {
                return planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(e)}, nil
            }
        }
        return planner.ToolResult{
            Name:   call.Name,
            Result: result,
        }, nil
    })
}


