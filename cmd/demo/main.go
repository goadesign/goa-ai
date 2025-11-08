package main

import (
	"context"
	"fmt"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
)

// stubPlanner is a tiny planner that immediately returns a final response.
type stubPlanner struct{}

func (p *stubPlanner) PlanStart(ctx context.Context, in planner.PlanInput) (*planner.PlanResult, error) {
	r := &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "Hello from Goa‑AI!"}}}
	return r, nil
}
func (p *stubPlanner) PlanResume(ctx context.Context, in planner.PlanResumeInput) (*planner.PlanResult, error) {
	r := &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "Done."}}}
	return r, nil
}

func main() {
	ctx := context.Background()

	// 1) Runtime (uses in-memory engine by default)
	rt := runtime.New()

	// 2) Register a minimal agent with activities bound to runtime methods
	const (
		agentID    = "demo.agent"
		wfName     = "demo.workflow"
		taskQueue  = "demo.queue"
		planAct    = "plan"
		resumeAct  = "resume"
		executeAct = "execute"
	)

	// Workflow handler delegates to runtime.ExecuteWorkflow
	wf := engine.WorkflowDefinition{
		Name:      wfName,
		TaskQueue: taskQueue,
		Handler:   runtime.WorkflowHandler(rt),
	}

	// Activities delegate to runtime activity methods
	activities := []engine.ActivityDefinition{
		{Name: planAct, Handler: runtime.PlanStartActivityHandler(rt)},
		{Name: resumeAct, Handler: runtime.PlanResumeActivityHandler(rt)},
		{Name: executeAct, Handler: runtime.ExecuteToolActivityHandler(rt)},
	}

	if err := rt.RegisterAgent(ctx, runtime.AgentRegistration{
		ID:                  agentID,
		Planner:             &stubPlanner{},
		Workflow:            wf,
		Activities:          activities,
		PlanActivityName:    planAct,
		ResumeActivityName:  resumeAct,
		ExecuteToolActivity: executeAct,
		Policy:              runtime.RunPolicy{MaxToolCalls: 0},
	}); err != nil {
		panic(err)
	}

	// 3) Run the agent using a route-based client (no local lookup required)
	client := rt.MustClientFor(runtime.AgentRoute{ID: agent.Ident(agentID), WorkflowName: wfName, DefaultTaskQueue: taskQueue})
	out, err := client.Run(ctx, []planner.AgentMessage{{Role: "user", Content: "Say hi"}}, runtime.WithSessionID("session-1"))
	if err != nil {
		panic(err)
	}
	fmt.Println("RunID:", out.RunID)
	fmt.Println("Assistant:", out.Final.Content)
}
