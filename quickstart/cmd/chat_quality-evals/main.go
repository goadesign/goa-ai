// Command chat_quality-evals runs the chat_quality evaluation suite.
//
// Capture runs the example agent and records its typed observations. Assessment
// reads those saved observations and runs the generated exact-check methods;
// it needs no live runtime, session store, or product hooks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	genevalchatquality "example.com/quickstart/gen/evals/chat_quality"
	genchat "example.com/quickstart/gen/orchestrator/agents/chat"
	genhelpers "example.com/quickstart/gen/orchestrator/toolsets/helpers"
	"example.com/quickstart/internal/agents/orchestrator/bootstrap"
	eval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/eval/evidence"
	model "goa.design/goa-ai/runtime/agent/model"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/stream"
	streambridge "goa.design/goa-ai/runtime/agent/stream/bridge"
)

type (
	// hooks executes scenarios against the real chat agent running on the
	// in-memory engine, exactly like cmd/orchestrator does.
	hooks struct {
		checks
		rt    *agentruntime.Runtime
		chat  agentruntime.AgentClient
		store *storageinmem.Store
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
		capturePath    string
		assessPath     string
		scenarios      values
		tags           values
	}
)

func main() {
	opts := options{}
	flag.IntVar(&opts.maxConcurrency, "max-concurrency", 5, "maximum scenarios to run concurrently")
	flag.StringVar(&opts.capturePath, "capture", "", "capture evidence into a new archive without assessment")
	flag.StringVar(&opts.assessPath, "assess", "", "assess an existing archive without running the agent")
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
	if opts.capturePath != "" && opts.assessPath != "" {
		return errors.New("--capture and --assess cannot be combined")
	}
	if opts.assessPath != "" && (len(opts.scenarios) > 0 || len(opts.tags) > 0) {
		return errors.New("--assess uses the scenarios recorded in the archive")
	}
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{MaxConcurrency: opts.maxConcurrency})
	if err != nil {
		return err
	}
	// An offline run constructs only the exact predicates. Product dependencies
	// are built below, after this branch has returned.
	if opts.assessPath != "" {
		file, err := os.Open(opts.assessPath)
		if err != nil {
			return err
		}
		archive, readErr := eval.ReadArchive(file)
		if err := errors.Join(readErr, file.Close()); err != nil {
			return err
		}
		suite, err := genevalchatquality.ForAssessment(checks{})
		if err != nil {
			return err
		}
		report, err := runner.Assess(ctx, suite, archive)
		return writeAssessment(report, err)
	}
	store := storageinmem.New()
	rt, cleanup, err := bootstrap.New(ctx, store)
	if err != nil {
		return fmt.Errorf("initialize runtime: %w", err)
	}
	defer cleanup()
	suite, err := genevalchatquality.New(&hooks{rt: rt, chat: genchat.NewClient(rt), store: store}, scenarioInputs())
	if err != nil {
		return err
	}
	var archive eval.Archive
	if opts.capturePath != "" {
		switch {
		case len(opts.scenarios) > 0:
			archive, err = runner.CaptureScenarios(ctx, suite, opts.scenarios...)
		case len(opts.tags) > 0:
			archive, err = runner.CaptureTags(ctx, suite, opts.tags...)
		default:
			archive, err = runner.Capture(ctx, suite)
		}
		return saveCapture(opts.capturePath, archive, err)
	}
	var report eval.Report
	switch {
	case len(opts.scenarios) > 0:
		_, report, err = runner.RunScenarios(ctx, suite, opts.scenarios...)
	case len(opts.tags) > 0:
		_, report, err = runner.RunTags(ctx, suite, opts.tags...)
	default:
		_, report, err = runner.Run(ctx, suite)
	}
	return writeAssessment(report, err)
}

// scenarioInputs supplies environment-specific values.
func scenarioInputs() genevalchatquality.Inputs {
	return genevalchatquality.Inputs{
		GreetingReply: &genevalchatquality.AskPayload{
			Question: "What is the capital of Japan?",
		},
	}
}

// GreetingReply runs the agent and copies observed facts into the declared
// record. Product failures remain observable; collector or subscription errors
// mean capture itself could not produce a complete record.
func (h *hooks) GreetingReply(ctx context.Context, input *genevalchatquality.AskPayload) (*genevalchatquality.GreetingObservation, error) {
	const sessionID = "eval-greeting-reply"
	if _, err := h.store.CreateSession(ctx, sessionID, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	collector := evidence.NewCollector()
	sub, err := streambridge.Register(
		h.rt.Bus,
		&collectorSink{sessionID: sessionID, collector: collector},
		stream.RuntimeHostProfile(),
	)
	if err != nil {
		return nil, fmt.Errorf("attach stream subscriber: %w", err)
	}
	_, runErr := h.chat.Run(ctx, sessionID, []*model.Message{
		{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: input.Question}},
		},
	}, agentruntime.WithRunID("eval-greeting-reply-run"))
	if closeErr := sub.Close(); closeErr != nil {
		return nil, fmt.Errorf("detach stream subscriber: %w", errors.Join(runErr, closeErr))
	}
	ev, err := collector.Finish()
	if err != nil {
		return nil, fmt.Errorf("finish evidence: %w", errors.Join(runErr, err))
	}
	return captureObservation(input.Question, ev, runErr), nil
}

// HelpersContract records whether the generated tool contract has a payload
// schema. The separate exact predicate assesses this saved boolean.
func (*hooks) HelpersContract(context.Context) (genevalchatquality.HelpersContractObservation, error) {
	spec := genevalchatquality.MustToolContract(genhelpers.Answer)
	return genevalchatquality.HelpersContractObservation(len(spec.Payload.Schema) > 0), nil
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

// saveCapture creates a new private archive and preserves partial evidence even
// when a scenario failed. Existing files are never overwritten.
func saveCapture(destination string, archive eval.Archive, captureErr error) error {
	if archive.ID == "" {
		return captureErr
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.Join(captureErr, err)
	}
	_, writeErr := archive.WriteTo(file)
	if err := errors.Join(captureErr, writeErr, file.Close()); err != nil {
		return err
	}
	for _, observation := range archive.Observations {
		if observation.Error != "" {
			return fmt.Errorf("scenario %q capture failed: %s", observation.ScenarioID, observation.Error)
		}
	}
	return nil
}

// writeAssessment emits one appendable JSON record, then reports failure to CI.
func writeAssessment(report eval.Report, assessmentErr error) error {
	if report.ID == "" {
		return assessmentErr
	}
	if _, err := report.WriteTo(os.Stdout); err != nil {
		return errors.Join(assessmentErr, err)
	}
	if assessmentErr != nil {
		return assessmentErr
	}
	if !report.Passed {
		return fmt.Errorf("evaluation suite %q failed", report.SuiteID)
	}
	return nil
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
