// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}MCPExecutor returns a ToolCallExecutor that
// proxies tool calls to an MCP caller using generated per-toolset codecs.
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}MCPExecutor(caller mcpruntime.Caller) runtime.ToolCallExecutor {
    suite := {{ printf "%q" .Toolset.QualifiedName }}
    prefix := suite + "."

    return runtime.ToolCallExecutorFunc(func(ctx context.Context, meta runtime.ToolCallMeta, call planner.ToolRequest) (planner.ToolResult, error) {
        full := call.Name
        tool := full
        if strings.HasPrefix(tool, prefix) {
            tool = tool[len(prefix):]
        }

        // Encode payload using generated codec
        if pc, ok := {{ $.Toolset.SpecsPackageName }}.PayloadCodec(full); ok {
            // When ExecuteToolActivity already decoded payload into a typed value,
            // PayloadCodec will encode it deterministically for transport.
            payload, err := pc.ToJSON(call.Payload)
            if err != nil {
                return planner.ToolResult{Name: full, Error: planner.ToolErrorFromError(err)}, nil
            }
            resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{Suite: suite, Tool: tool, Payload: payload})
            if err != nil {
                return planner.ToolResult{Name: full, Error: planner.ToolErrorFromError(err)}, nil
            }
            var value any
            if len(resp.Result) > 0 {
                if rc, ok := {{ $.Toolset.SpecsPackageName }}.ResultCodec(full); ok {
                    v, err := rc.FromJSON(resp.Result)
                    if err != nil {
                        return planner.ToolResult{Name: full, Error: planner.ToolErrorFromError(err)}, nil
                    }
                    value = v
                }
            }
            var tel *telemetry.ToolTelemetry
            if len(resp.Structured) > 0 {
                tel = &telemetry.ToolTelemetry{Extra: map[string]any{"structured": json.RawMessage(resp.Structured)}}
            }
            return planner.ToolResult{Name: full, Result: value, Telemetry: tel}, nil
        }
        return planner.ToolResult{Name: full, Error: planner.NewToolError("payload codec not found")}, nil
    })
}


