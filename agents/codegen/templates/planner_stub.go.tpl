// new{{ .GoName }}Planner returns a trivial planner implementation used by the example bootstrap.
func new{{ .GoName }}Planner() planner.Planner { return &example{{ .GoName }}Planner{} }

type example{{ .GoName }}Planner struct{}

func (p *example{{ .GoName }}Planner) PlanStart(ctx context.Context, in planner.PlanInput) (planner.PlanResult, error) {
	// Minimal example: produce a generic assistant reply when no tools are requested.
	return planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "Hello from example planner."}}}, nil
}

func (p *example{{ .GoName }}Planner) PlanResume(ctx context.Context, in planner.PlanResumeInput) (planner.PlanResult, error) {
	return planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "Done."}}}, nil
}
