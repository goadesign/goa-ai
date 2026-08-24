{{- if .Register }}
// Register registers the method-backed toolset with the runtime using Execute.
func Register(ctx context.Context, rt *runtime.Runtime) error {
    if rt == nil {
        return errors.New("runtime is required")
    }
    reg := {{ $.AgentImport.Name }}.New{{ $.Agent.GoName }}{{ goify $.Toolset.PathName true }}ToolsetRegistration(runtime.ToolCallExecutorFunc(Execute))
    return rt.RegisterToolset(reg)
}
{{- end }}

// Execute checks one tool call against its generated argument contract. The
// initial implementation returns the result example from the design;
// applications replace that result with their service call.
func Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
    if call == nil {
        return nil, errors.New("tool request is nil")
    }
    if meta == nil {
        return nil, errors.New("tool call meta is nil")
    }
    switch call.Name {
    {{- range .Tools }}
    case "{{ .ID }}":
        // Decode the JSON arguments with the generated {{ .ID }} contract.
        {{- if .InjectDecodeFunc }}
        _, err := {{ $.SpecsAlias }}.{{ .InjectDecodeFunc }}(call.Payload, *meta, meta.Labels)
        {{- else }}
        _, err := {{ $.SpecsAlias }}.{{ .TypedTool }}().Payload.FromJSON(call.Payload)
        {{- end }}
        if err != nil {
            var issuer interface {
                Issues() []*tools.FieldIssue
            }
            var issues []*tools.FieldIssue
            if errors.As(err, &issuer) {
                issues = issuer.Issues()
            }
            return runtime.Executed(&planner.ToolResult{
                Name: call.Name,
                Failure: &planner.ToolFailure{
                    Kind: planner.FailureInvalidCall,
                    Error: planner.ToolErrorFromError(err),
                    Recovery: planner.RecoveryDirective{
                        Action: planner.RecoveryCorrectCall,
                        Issues: issues,
                    },
                },
			}), nil
        }
        {{- if .HasResult }}
        {{- if .HasResultExample }}
        result, err := {{ $.SpecsAlias }}.{{ .TypedTool }}().Result.FromJSON(
            rawjson.Message({{ printf "%q" .ResultExample }}),
        )
        if err != nil {
            return nil, fmt.Errorf("decode {{ .ID }} example result: %w", err)
        }
        return runtime.Executed(&planner.ToolResult{
            Name:   call.Name,
            Result: result,
		}), nil
        {{- else }}
        return nil, fmt.Errorf("execute %s: generated executor requires an application implementation", call.Name)
        {{- end }}
        {{- else }}
        return runtime.Executed(&planner.ToolResult{Name: call.Name}), nil
        {{- end }}
    {{- end }}
    default:
        return runtime.Executed(&planner.ToolResult{
            Name: call.Name,
            Failure: &planner.ToolFailure{
                Kind: planner.FailureInvalidCall,
                Error: planner.NewToolError("unknown tool"),
                Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan},
            },
		}), nil
    }
}
