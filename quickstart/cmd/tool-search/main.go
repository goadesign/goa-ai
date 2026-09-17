// Command tool-search runs the generated quickstart agent with native model
// discovery. Its helper executor still returns the fixed quickstart result.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	genchat "example.com/quickstart/gen/orchestrator/agents/chat"
	"example.com/quickstart/internal/agents/chat/toolsets/helpers"
	"goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/features/model/openai"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type (
	// searchPlanner sends the generated, policy-filtered catalog to the model.
	// The adapter handles search and returns ordinary tool calls.
	searchPlanner struct {
		modelID string
	}
)

func main() {
	provider := flag.String("provider", "openai", "Model provider: openai or anthropic")
	modelID := flag.String("model", "", "Model ID supporting native tool search (required)")
	flag.Parse()
	if *modelID == "" {
		log.Fatal("-model is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var client model.Client
	var err error
	switch *provider {
	case "openai":
		client, err = openai.NewFromAPIKey(os.Getenv("OPENAI_API_KEY"), *modelID)
	case "anthropic":
		client, err = anthropic.NewFromAPIKey(os.Getenv("ANTHROPIC_API_KEY"), *modelID)
	default:
		log.Fatalf("unknown provider %q", *provider)
	}
	if err != nil {
		log.Fatal(err)
	}
	if err := run(ctx, client, *modelID, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func (p searchPlanner) PlanStart(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	return p.plan(ctx, input.Agent, input.Messages, false)
}

func (p searchPlanner) PlanResume(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return p.plan(ctx, input.Agent, input.Messages, input.SynthesisOnly || input.Finalize != nil)
}

func run(ctx context.Context, client model.Client, modelID string, output io.Writer) error {
	store := storageinmem.New()
	if _, err := store.CreateSession(ctx, "tool-search-demo", time.Now().UTC()); err != nil {
		return err
	}
	rt := runtime.New(store)
	if err := rt.RegisterModel("primary", client); err != nil {
		return err
	}
	if err := genchat.RegisterChatAgent(ctx, rt, genchat.ChatAgentConfig{Planner: searchPlanner{modelID: modelID}}); err != nil {
		return err
	}
	if err := genchat.RegisterUsedToolsets(ctx, rt,
		genchat.WithHelpersExecutor(runtime.ToolCallExecutorFunc(helpers.Execute)),
	); err != nil {
		return err
	}
	result, err := genchat.NewClient(rt).Run(ctx, "tool-search-demo", []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{
			Text: "Use the available question-answering helper to find the capital of Japan, then report its answer.",
		}},
	}}, runtime.WithRunID("tool-search-run"), runtime.WithRunTimeBudget(2*time.Minute))
	if err != nil {
		return err
	}
	for _, part := range result.Final.Parts {
		if text, ok := part.(model.TextPart); ok {
			if _, err := fmt.Fprintln(output, text.Text); err != nil {
				return err
			}
		}
	}
	return nil
}

// plan preserves the runtime's messages and definitions. Native search rounds
// share one 4096-token output budget; they never become planner tool requests.
func (p searchPlanner) plan(ctx context.Context, agent planner.PlannerContext, messages []*model.Message, final bool) (*planner.PlanResult, error) {
	client, ok := agent.PlannerModelClient("primary")
	if !ok {
		return nil, errors.New("primary model is not registered")
	}
	request := &model.Request{
		Model:    p.modelID,
		Messages: messages, Tools: agent.AdvertisedToolDefinitions(),
		MaxTokens: 4096, Stream: true,
	}
	if final {
		request.ToolChoice = &model.ToolChoice{Mode: model.ToolChoiceModeNone}
	}
	summary, err := client.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(summary.ToolCalls) > 0 {
		return &planner.PlanResult{ToolCalls: summary.ToolCalls}, nil
	}
	return &planner.PlanResult{FinalResponse: summary.FinalResponse()}, nil
}
