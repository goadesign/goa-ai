// new{{ .GoName }}Planner returns a trivial planner implementation used by the example bootstrap.
func new{{ .GoName }}Planner() planner.Planner {
    return &example{{ .GoName }}Planner{}
}

type example{{ .GoName }}Planner struct{}

// example{{ .GoName }}Planner is a minimal, stateless implementation of planner.Planner
// used by the example bootstrap for {{ .GoName }}. The planner is the agent's
// decision-making core: it analyzes conversation messages, chooses whether to
// execute tools, and produces the final assistant response. In production you
// would replace this with logic that calls your LLM via registered model clients
// (e.g., in.Agent.ModelClient("<id>")).

func (p *example{{ .GoName }}Planner) PlanStart(ctx context.Context, in planner.PlanInput) (*planner.PlanResult, error) {
    // Minimal example: produce a generic assistant reply when no tools are requested.
    return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "Hello from example planner."}}}, nil
}

func (p *example{{ .GoName }}Planner) PlanResume(ctx context.Context, in planner.PlanResumeInput) (*planner.PlanResult, error) {
    return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "Done."}}}, nil
}
