{{/* cmd_main.go.tpl - generates cmd/<service>/main.go for agent-only designs */}}
// This example main demonstrates running the agent using the generated bootstrap.
// Replace or extend this as needed for your production deployment.

func main() {
	ctx := context.Background()

	// Initialize the runtime using the generated bootstrap which wires
	// planners and toolsets for all agents.
	rt, cleanup, err := bootstrap.New(ctx)
	if err != nil {
		log.Fatalf("failed to initialize runtime: %v", err)
	}
	defer cleanup()

	// Example: run the first registered agent with a simple message.
	// Replace this with your own CLI, HTTP server, or integration.
	// Sessions are first-class: create a session explicitly before starting runs.
	// Creating an already-active session is idempotent.
	if _, err := rt.CreateSession(ctx, "demo-session"); err != nil {
		log.Fatalf("failed to create session: %v", err)
	}

{{ range .Agents }}
	{
		client := {{ .PackageName }}.NewClient(rt)
		out, err := client.Run(ctx, "demo-session", []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "Hello"}},
			},
		})
		if err != nil {
			log.Fatalf("agent run failed: %v", err)
		}
		fmt.Println("RunID:", out.RunID)
		if out.Final != nil && len(out.Final.Parts) > 0 {
			if tp, ok := out.Final.Parts[0].(model.TextPart); ok {
				fmt.Println("Assistant:", tp.Text)
			}
		}
	}
{{ end }}
}
