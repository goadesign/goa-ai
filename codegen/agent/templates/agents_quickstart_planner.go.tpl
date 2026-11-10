---

## 4. 🧠 The Planner: Giving Your Agent a Brain

The `Planner` is where your agent's intelligence lives. It connects to an LLM to decide what to do next. The `StubPlanner` above is great for testing, but here's the correct interface for a real implementation.

```go
type MySmartPlanner struct{}

// PlanStart is called at the beginning of a run.
func (p *MySmartPlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
    // 1. Get an LLM client from the runtime.
    // mc, _ := in.Agent.PlannerModelClient("bedrock")
    
    // 2. Read in.Messages to inspect saved history or build your model prompt.
    
    // 3. Call the LLM and decide whether to call tools or give a final answer.
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "I'm ready to help!"}},
			},
        },
    }, nil
}

// PlanResume is called after tools have run, giving the agent new information.
func (p *MySmartPlanner) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
    // 1. Inspect the tool results from in.ToolOutputs.
    // 2. Read in.Messages if conversation history is needed for the next prompt.
    // 3. Call the LLM to decide what to do next.
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "The tools have run. Here's what I found..."}},
			},
        },
    }, nil
}
```

`Messages` contains the saved conversation and must be treated as read-only.
The runtime applies history policy separately to each actual model request,
using that destination model's token counter. Reading messages, retrieving a
client, and making a decision without calling a model do not count or summarize
history. Return errors from `Complete` or `Stream` normally.

