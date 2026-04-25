// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}MCPExecutor returns a ToolCallExecutor that
// proxies tool calls to an MCP caller using generated per-toolset codecs.
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}MCPExecutor(caller mcpruntime.Caller) runtime.ToolCallExecutor {
    suite := {{ printf "%q" .Toolset.QualifiedName }}

    return runtime.ToolCallExecutorFunc(func(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*runtime.ToolExecutionResult, error) {
        if call == nil {
            return runtime.Executed(&planner.ToolResult{Error: planner.NewToolError("tool request is nil")}), nil
        }
        if meta == nil {
            return runtime.Executed(&planner.ToolResult{Error: planner.NewToolError("tool call meta is nil")}), nil
        }
        switch call.Name {
        {{- range .Tools }}
        case {{ $.Toolset.SpecsPackageName }}.{{ .ConstName }}:
            resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
				Suite:   suite,
				Tool:    {{ printf "%q" .LocalName }},
				Payload: json.RawMessage(call.Payload),
			})
            if err != nil {
                return runtime.Executed(&planner.ToolResult{
					Name:  call.Name,
					Error: planner.ToolErrorFromError(err),
				}), nil
            }
            var value any
            {{- if .HasResult }}
            if len(resp.Result) > 0 {
                v, err := {{ $.Toolset.SpecsPackageName }}.{{ .ResultGenericCodec }}.FromJSON(resp.Result)
                if err != nil {
                    return runtime.Executed(&planner.ToolResult{
					Name:  call.Name,
					Error: planner.ToolErrorFromError(err),
				}), nil
                }
                value = v
            }
            {{- end }}
            var tel *telemetry.ToolTelemetry
            if len(resp.Structured) > 0 {
                tel = &telemetry.ToolTelemetry{
					Extra: map[string]any{
						"structured": json.RawMessage(resp.Structured),
					},
				}
            }
            return runtime.Executed(&planner.ToolResult{
				Name:      call.Name,
				Result:    value,
				Telemetry: tel,
			}), nil
        {{- end }}
        default:
            return runtime.Executed(&planner.ToolResult{
			    Name:  call.Name,
			    Error: planner.NewToolError("unknown MCP tool"),
		    }), nil
        }
    })
}


