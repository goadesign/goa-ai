// {{ $.Toolset.Name }} executor stub for {{ $.Agent.StructName }}
//
// This file declares a minimal executor implementation for the method-backed
// toolset {{ $.Toolset.Name }}. Replace the TODOs with real client calls and
// optional transforms. Keep business logic out of main; import this package
// from your service bootstrap when wiring agents.
//
// Header above defines package and imports; code below focuses on logic.

// Register registers the toolset with the runtime using the Execute stub below.
// Replace Execute with a real implementation that calls the bound service client
// and uses generated transforms when available.
func Register(ctx context.Context, rt *runtime.Runtime) error {
    if rt == nil {
        return errors.New("runtime is required")
    }
    reg := {{ $.AgentImport.Name }}.New{{ $.Agent.GoName }}{{ goify $.Toolset.PathName true }}ToolsetRegistration(runtime.ToolCallExecutorFunc(Execute))
    return rt.RegisterToolset(reg)
}

// Execute demonstrates per-tool branching with typed decode. Replace placeholders
// with client calls and optional transforms from the specs package (transforms.go).
func Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*runtime.ToolExecutionResult, error) {
    if call == nil {
        return nil, errors.New("tool request is nil")
    }
    if meta == nil {
        return nil, errors.New("tool call meta is nil")
    }
    switch call.Name {
    {{- range .Tools }}
    case "{{ .ID }}":
        // Decode typed payload
        args, err := {{ $.SpecsAlias }}.{{ .PayloadUnmarshal }}(call.Payload)
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
                            {{ $.SpecsAlias }}.Spec{{ .ConstName }}.Payload.ExampleJSON...,
                        ),
                    },
                },
			}), nil
        }
        // Optional: transform to method payload if compatible
        // mp, _ := {{ $.SpecsAlias }}.ToMethodPayload_{{ .GoName }}(args)
        // TODO: Call your service client with mp (or args), map result back:
        // tr, _ := {{ $.SpecsAlias }}.ToToolReturn_{{ .GoName }}(methodRes)
        return runtime.Executed(&planner.ToolResult{
			Result: map[string]any{
				"status": "ok",
			},
		}), nil
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
