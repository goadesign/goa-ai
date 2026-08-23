// New returns a minimal planner implementation for {{ .Agent.GoName }} used by example bootstrap.
// Replace with your production planner integrating your LLM of choice.
func New() planner.Planner { return &examplePlanner{} }

// examplePlanner is a minimal, stateless implementation of planner.Planner used by
// the example bootstrap. The planner acts as the agent's "brain": it reasons over
// conversation messages, decides whether to request tool executions, and produces
// final assistant responses. Production planners typically invoke LLMs via
// registered planner-scoped model clients (in.Agent.PlannerModelClient(...)) or
// pair ModelClient with planner.ConsumeStream, which only drains the validated
// stream. The runtime journal publishes selected presentation and usage.
type examplePlanner struct{}

func (p *examplePlanner) Decide(ctx context.Context, tool string) string {
	{{- if .Agent.Methods }}
	switch tool {
	{{- range .Agent.Methods }}
	case "{{ .Name }}":
		{{- if .Passthrough }}
		return "passthrough"
		{{- else }}
		return "reason"
		{{- end }}
	{{- end }}
	default:
		return "reason"
	}
	{{- else }}
	return "reason"
	{{- end }}
}

func (p *examplePlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	// Check for deterministic routing (Passthrough)
	if in.RunContext.Tool != "" {
		route := p.Decide(ctx, string(in.RunContext.Tool))
		if route == "passthrough" {
			// Deterministic passthrough: forward args directly to the target tool.
			// For this example stub, we don't have the target mapping (it's in the DSL but not fully exposed here yet).
			// Real implementation would use the Passthrough metadata to construct the ToolRequest.
			// This stub just falls through to the default response.
		}
	}
	{{- if .Tool }}
	args, err := gentool.{{ .Tool.Payload.ExportedCodec }}.FromJSON(gentool.Spec{{ .Tool.ConstName }}.Payload.ExampleJSON)
	if err != nil {
		return nil, fmt.Errorf("decode generated {{ .Tool.Name }} example: %w", err)
	}
	return &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			gentool.New{{ .Tool.GoName }}Call("quickstart-{{ .Tool.Name }}", args),
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

func (p *examplePlanner) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	{{- if .Tool }}
	if len(in.ToolOutputs) != 1 {
		return nil, fmt.Errorf("expected one {{ .Tool.Name }} result, got %d", len(in.ToolOutputs))
	}
	output := in.ToolOutputs[0]
	if output.Name != gentool.{{ .Tool.ConstName }} || output.ToolCallID != "quickstart-{{ .Tool.Name }}" {
		return nil, fmt.Errorf("unexpected tool result %s (%s)", output.Name, output.ToolCallID)
	}
	if output.Failure != nil {
		return nil, fmt.Errorf("{{ .Tool.Name }} failed: %s", output.Failure.Error.Message)
	}
	answer := fmt.Sprintf("Tool %s returned %s", output.Name, output.Result)
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
