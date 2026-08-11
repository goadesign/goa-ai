// Command chat_quality-evals runs the chat_quality evaluation suite.
//
// This file was generated once by goa example. It is application-owned:
// subsequent generation leaves product execution and assertions unchanged.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	genevalchatquality "example.com/quickstart/gen/evals/chat_quality"
	genchat "example.com/quickstart/gen/orchestrator/agents/chat"
	"example.com/quickstart/internal/agents/bootstrap"
	eval "goa.design/goa-ai/eval"
	model "goa.design/goa-ai/runtime/agent/model"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	tools "goa.design/goa-ai/runtime/agent/tools"
)

type (
	// hooks executes scenarios against the real chat agent running on the
	// in-memory engine, exactly like cmd/orchestrator does.
	hooks struct {
		chat agentruntime.AgentClient
	}

	values []string

	options struct {
		maxConcurrency int
		scenarios      values
		tags           values
	}
)

func main() {
	opts := options{}
	flag.IntVar(&opts.maxConcurrency, "max-concurrency", 5, "maximum scenarios to run concurrently")
	flag.Var(&opts.scenarios, "scenario", "scenario ID to run; repeat to select multiple scenarios")
	flag.Var(&opts.tags, "tag", "scenario tag to run; repeat to select multiple tags")
	flag.Parse()
	if err := run(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if len(opts.scenarios) > 0 && len(opts.tags) > 0 {
		return errors.New("--scenario and --tag cannot be combined")
	}
	rt, cleanup, err := bootstrap.New(ctx)
	if err != nil {
		return fmt.Errorf("initialize runtime: %w", err)
	}
	defer cleanup()
	suite, err := genevalchatquality.New(&hooks{chat: genchat.NewClient(rt)}, scenarioInputs())
	if err != nil {
		return err
	}
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{
		MaxConcurrency: opts.maxConcurrency,
		// TODO: replace nil above with an eval.Judge when hooks return semantic
		// claims. Nil is valid only for deterministic-only suites.
		// TODO: supply an eval.Reporter for progressive application-specific output.
	})
	if err != nil {
		return err
	}
	var report eval.Report
	switch {
	case len(opts.scenarios) > 0:
		report, err = runner.RunScenarios(ctx, suite, opts.scenarios...)
	case len(opts.tags) > 0:
		report, err = runner.RunTags(ctx, suite, opts.tags...)
	default:
		report, err = runner.Run(ctx, suite)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		return errors.Join(err, fmt.Errorf("encode evaluation report: %w", encodeErr))
	}
	if err != nil {
		return err
	}
	if !report.Passed {
		return fmt.Errorf("evaluation suite %q failed", report.SuiteID)
	}
	return nil
}

// scenarioInputs supplies environment-specific values.
func scenarioInputs() genevalchatquality.Inputs {
	return genevalchatquality.Inputs{
		GreetingReply: &genevalchatquality.AskPayload{
			Question: "What is the capital of Japan?",
		},
	}
}

// GreetingReply executes the greeting_reply scenario: it runs the chat agent
// with the typed scenario input and asserts a final assistant reply exists.
// Hooks returning eval.Claims (judged by an eval.Judge) belong here too once a
// real model client is wired.
func (h *hooks) GreetingReply(ctx context.Context, input *genevalchatquality.AskPayload) (eval.Result, error) {
	out, err := h.chat.OneShotRun(ctx, []*model.Message{
		{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: input.Question}},
		},
	})
	if err != nil {
		return eval.Result{}, fmt.Errorf("run chat agent: %w", err)
	}
	check := eval.Check{Name: "final_reply_present", Passed: finalText(out) != ""}
	if !check.Passed {
		check.Diagnostic = "chat run finished without a final assistant reply"
	}
	return eval.Result{Checks: []eval.Check{check}}, nil
}

// HelpersContract executes the helpers_contract scenario: it asserts the
// helpers.answer tool contract generated for this agent carries a payload
// schema hooks can assert tool calls against.
func (*hooks) HelpersContract(context.Context) (eval.Result, error) {
	spec := genevalchatquality.MustToolContract(tools.Ident("helpers.answer"))
	check := eval.Check{Name: "answer_payload_schema_present", Passed: len(spec.Payload.Schema) > 0}
	if !check.Passed {
		check.Diagnostic = "helpers.answer contract has no payload schema"
	}
	return eval.Result{Checks: []eval.Check{check}}, nil
}

// finalText returns the text of the run's final assistant message, if any.
func finalText(out *agentruntime.RunOutput) string {
	if out.Final == nil || len(out.Final.Parts) == 0 {
		return ""
	}
	text, ok := out.Final.Parts[0].(model.TextPart)
	if !ok {
		return ""
	}
	return text.Text
}
func (v *values) String() string {
	return fmt.Sprint([]string(*v))
}

func (v *values) Set(value string) error {
	if value == "" {
		return errors.New("selector must not be empty")
	}
	*v = append(*v, value)
	return nil
}
