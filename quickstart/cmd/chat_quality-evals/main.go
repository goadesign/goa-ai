// Command chat_quality-evals runs the chat_quality evaluation suite.
//
// This file was scaffolded once by goa example and is application-owned:
// subsequent generation leaves product execution and assertions unchanged.
// The greeting_reply hook demonstrates the framework evidence flow: it
// bridges the runtime's event bus into an evidence.Collector, then grades
// the run with declarative expectations built from the generated typed tool
// descriptors.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"

	genevalchatquality "example.com/quickstart/gen/evals/chat_quality"
	genchat "example.com/quickstart/gen/orchestrator/agents/chat"
	genhelpers "example.com/quickstart/gen/orchestrator/toolsets/helpers"
	"example.com/quickstart/internal/agents/bootstrap"
	eval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/eval/evidence"
	model "goa.design/goa-ai/runtime/agent/model"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/stream"
	streambridge "goa.design/goa-ai/runtime/agent/stream/bridge"
)

type (
	// hooks executes scenarios against the real chat agent running on the
	// in-memory engine, exactly like cmd/orchestrator does.
	hooks struct {
		rt   *agentruntime.Runtime
		chat agentruntime.AgentClient
	}

	// collectorSink feeds one scenario's stream events into its evidence
	// collector. The runtime bus is process-global, so the sink filters by
	// session to isolate concurrent scenarios and serializes Consume because
	// bus delivery may span goroutines.
	collectorSink struct {
		sessionID string
		collector *evidence.Collector
		mu        sync.Mutex
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
	suite, err := genevalchatquality.New(&hooks{rt: rt, chat: genchat.NewClient(rt)}, scenarioInputs())
	if err != nil {
		return err
	}
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{
		MaxConcurrency: opts.maxConcurrency,
		// TODO: replace nil above with an eval.Judge when hooks return semantic
		// claims: construct judge.New (goa.design/goa-ai/eval/judge) with your
		// real model.Client. Nil is valid only for deterministic-only suites.
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
// while collecting the run's stream events as evidence, then grades the
// trajectory with typed expectations. evidence.ExpectCall binds the
// generated descriptor genhelpers.AnswerTool, so the predicates below are
// compile-checked against the tool's actual payload and result types. Hooks
// returning eval.Claims (judged by an eval.Judge) belong here too once a
// real model client is wired.
func (h *hooks) GreetingReply(ctx context.Context, input *genevalchatquality.AskPayload) (eval.Result, error) {
	const sessionID = "eval-greeting-reply"
	if _, err := h.rt.CreateSession(ctx, sessionID); err != nil {
		return eval.Result{}, fmt.Errorf("create session: %w", err)
	}
	collector := evidence.NewCollector()
	sub, err := streambridge.Register(h.rt.Bus, &collectorSink{sessionID: sessionID, collector: collector})
	if err != nil {
		return eval.Result{}, fmt.Errorf("attach stream subscriber: %w", err)
	}
	_, runErr := h.chat.Run(ctx, sessionID, []*model.Message{
		{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: input.Question}},
		},
	})
	if closeErr := sub.Close(); closeErr != nil {
		return eval.Result{}, fmt.Errorf("detach stream subscriber: %w", closeErr)
	}
	if runErr != nil {
		return eval.Result{}, fmt.Errorf("run chat agent: %w", runErr)
	}
	ev, err := collector.Finish()
	if err != nil {
		return eval.Result{}, fmt.Errorf("finish evidence: %w", err)
	}
	expect := evidence.Expect{
		Tools: []evidence.Tool{
			evidence.ExpectCall(genhelpers.AnswerTool,
				func(p *genhelpers.AnswerPayload) error {
					if p.Question != input.Question {
						return fmt.Errorf("question: got %q, want %q", p.Question, input.Question)
					}
					return nil
				},
				func(r *genhelpers.AnswerResult) error {
					if r.Text == "" {
						return errors.New("answer text must not be empty")
					}
					return nil
				},
			),
		},
	}
	return eval.Result{Checks: expect.Checks(ev), Output: ev.Answer}, nil
}

// HelpersContract executes the helpers_contract scenario: it asserts the
// helpers.answer tool contract generated for this agent carries a payload
// schema hooks can assert tool calls against.
func (*hooks) HelpersContract(context.Context) (eval.Result, error) {
	spec := genevalchatquality.MustToolContract(genhelpers.Answer)
	check := eval.Check{Name: "answer_payload_schema_present", Passed: len(spec.Payload.Schema) > 0}
	if !check.Passed {
		check.Diagnostic = "helpers.answer contract has no payload schema"
	}
	return eval.Result{Checks: []eval.Check{check}}, nil
}

// Send feeds session-scoped stream events into the collector.
func (s *collectorSink) Send(_ context.Context, event stream.Event) error {
	if event.SessionID() != s.sessionID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collector.Consume(event)
}

// Close implements stream.Sink; the collector owns no transport resources.
func (s *collectorSink) Close(context.Context) error { return nil }

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
