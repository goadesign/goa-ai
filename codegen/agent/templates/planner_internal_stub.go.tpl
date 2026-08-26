// New returns the example planner registered by the generated bootstrap.
// Replace it with the planner that should answer real application requests.
func New() planner.Planner { return &examplePlanner{} }

// examplePlanner makes one sample tool call and turns its result into an
// assistant reply. It lets a new application run without model credentials.
type examplePlanner struct{}

// PlanStart requests the example tool call selected from the design.
func (*examplePlanner) PlanStart(_ context.Context, _ *planner.PlanInput) (*planner.PlanResult, error) {
	{{- if .Tool }}
    args, err := gentool.{{ .Tool.Payload.ExportedCodec }}().FromJSON(gentool.{{ .Tool.SpecVar }}().Payload.ExampleJSON)
	if err != nil {
		return nil, fmt.Errorf("decode generated {{ .Tool.Name }} example: %w", err)
	}
    call, err := planner.NewToolRequest(gentool.{{ .Tool.TypedToolVar }}(), args)
	if err != nil {
		return nil, fmt.Errorf("build generated {{ .Tool.Name }} call: %w", err)
	}
	return &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			call,
		},
	}, nil
	{{- else }}
    return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "Hello from example planner."}},
			},
		},
	}, nil
	{{- end }}
}

// PlanResume turns the example tool result into the assistant's final reply.
func (*examplePlanner) PlanResume(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	{{- if .Tool }}
	if len(in.ToolOutputs) != 1 {
		return nil, fmt.Errorf("expected one {{ .Tool.Name }} result, got %d", len(in.ToolOutputs))
	}
	output := in.ToolOutputs[0]
	if output.Name != gentool.{{ .Tool.ConstName }} {
		return nil, fmt.Errorf("unexpected tool result %s (%s)", output.Name, output.ToolCallID)
	}
	if output.Failure != nil {
		if output.Failure.Error.Cause != nil {
			return nil, fmt.Errorf(
				"{{ .Tool.Name }} failed: %s: %s",
				output.Failure.Error.Message,
				output.Failure.Error.Cause.Message,
			)
		}
		return nil, fmt.Errorf("{{ .Tool.Name }} failed: %s", output.Failure.Error.Message)
	}
	{{- if .Tool.HasResult }}
	answer := fmt.Sprintf("Tool %s returned %s", output.Name, output.Result)
	{{- else }}
	answer := fmt.Sprintf("Tool %s completed successfully", output.Name)
	{{- end }}
	{{- else }}
	answer := "Done."
	{{- end }}
    return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: answer}},
			},
		},
    }, nil
}
