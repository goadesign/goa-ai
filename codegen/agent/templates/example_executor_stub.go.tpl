// {{ $.Toolset.Name }} executor stub for {{ $.Agent.StructName }}
//
// This starter runs the tools in {{ $.Toolset.Name }} that are handled by
// service methods. Replace each TODO with the matching service call. Import
// this package from the code that starts the agents.

// Register makes the toolset available to the agent runtime.
func Register(ctx context.Context, rt *runtime.Runtime) error {
    if rt == nil {
        return errors.New("runtime is required")
    }
    reg := {{ $.AgentImport.Name }}.New{{ $.Agent.GoName }}{{ goify $.Toolset.PathName true }}ToolsetRegistration(runtime.ToolCallExecutorFunc(Execute))
    return rt.RegisterToolset(reg)
}

// Execute validates each tool input before the application calls its service.
func Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*runtime.ToolExecutionResult, error) {
    if call == nil {
        return nil, errors.New("tool request is nil")
    }
    if meta == nil {
        return nil, errors.New("tool call meta is nil")
    }
    switch call.Name {
    {{- range .Tools }}
    {{- if .Tool.IsMethodBacked }}
    case "{{ .Tool.Name }}":
        // Decode and validate the payload. Keep the returned value when adding
        // the service call below.
        _, err := {{ $.SpecsAlias }}.{{ .Spec.Payload.ExportedCodec }}.FromJSON(call.Payload)
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
                        PriorInput: append(rawjson.Message(nil), call.Payload...),
                        ExampleJSON: append(
                            rawjson.Message(nil),
                            {{ $.SpecsAlias }}.{{ .Spec.SpecVar }}.Payload.ExampleJSON...,
                        ),
                    },
                },
			}), nil
        }
        // TODO: Call the service and return its result.
        return runtime.Executed(&planner.ToolResult{
			Result: map[string]any{
				"status": "ok",
			},
		}), nil
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

