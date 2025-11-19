// Package interrupt provides workflow signal handling for pausing and resuming
// agent runs. It exposes a Controller that workflows can use to react to
// external pause/resume requests via Temporal signals.
package interrupt

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

const (
	// SignalPause is the workflow signal name used to pause a run.
	SignalPause = "goaai.runtime.pause"
	// SignalResume is the workflow signal name used to resume a paused run.
	SignalResume = "goaai.runtime.resume"

	// SignalProvideClarification delivers a ClarificationAnswer to a waiting run.
	SignalProvideClarification = "goaai.runtime.provide.clarification"
	// SignalProvideToolResults delivers external tool results to a waiting run.
	SignalProvideToolResults = "goaai.runtime.provide.toolresults"
)

type (
	// PauseRequest carries metadata attached to a pause signal.
	PauseRequest struct {
		RunID       string
		Reason      string
		RequestedBy string
		Labels      map[string]string
		Metadata    map[string]any
	}

	// ResumeRequest carries metadata attached to a resume signal.
	ResumeRequest struct {
		RunID       string
		Notes       string
		RequestedBy string
		Labels      map[string]string
		// Messages allows human or policy actors to inject new conversational
		// messages before the planner resumes execution.
		Messages []*model.Message
	}

	// Controller drains runtime interrupt signals and exposes helpers the
	// workflow loop can call to react to pause/resume requests.
	Controller struct {
		pauseCh   engine.SignalChannel
		resumeCh  engine.SignalChannel
		clarifyCh engine.SignalChannel
		resultsCh engine.SignalChannel
	}
)

// NewController builds a controller wired to the workflow context signals.
func NewController(wfCtx engine.WorkflowContext) *Controller {
	return &Controller{
		pauseCh:   wfCtx.SignalChannel(SignalPause),
		resumeCh:  wfCtx.SignalChannel(SignalResume),
		clarifyCh: wfCtx.SignalChannel(SignalProvideClarification),
		resultsCh: wfCtx.SignalChannel(SignalProvideToolResults),
	}
}

// PollPause attempts to dequeue a pause request without blocking.
func (c *Controller) PollPause() (PauseRequest, bool) {
	if c == nil || c.pauseCh == nil {
		return PauseRequest{}, false
	}
	var req PauseRequest
	if !c.pauseCh.ReceiveAsync(&req) {
		return PauseRequest{}, false
	}
	return req, true
}

// WaitResume blocks until a resume request is delivered. Returns an error if the
// controller was not initialized with a resume signal channel.
func (c *Controller) WaitResume(ctx context.Context) (ResumeRequest, error) {
	if c == nil || c.resumeCh == nil {
		return ResumeRequest{}, errors.New("interrupt: resume channel unavailable")
	}
	var req ResumeRequest
	if err := c.resumeCh.Receive(ctx, &req); err != nil {
		return ResumeRequest{}, err
	}
	return req, nil
}

// ClarificationAnswer carries a typed answer for a paused clarification request.
type ClarificationAnswer struct {
	RunID  string
	ID     string
	Answer string
	Labels map[string]string
}

// ToolResultsSet carries results for an external tools await request.
type ToolResultsSet struct {
	RunID   string
	ID      string
	Results []*planner.ToolResult
	// RetryHints optionally provides hints associated with failures.
	RetryHints []*planner.RetryHint
}

// WaitProvideClarification blocks until a clarification answer is delivered.
func (c *Controller) WaitProvideClarification(ctx context.Context) (ClarificationAnswer, error) {
	if c == nil || c.clarifyCh == nil {
		return ClarificationAnswer{}, errors.New("interrupt: clarification channel unavailable")
	}
	var ans ClarificationAnswer
	if err := c.clarifyCh.Receive(ctx, &ans); err != nil {
		return ClarificationAnswer{}, err
	}
	return ans, nil
}

// WaitProvideToolResults blocks until external tool results are delivered.
func (c *Controller) WaitProvideToolResults(ctx context.Context) (ToolResultsSet, error) {
	if c == nil || c.resultsCh == nil {
		return ToolResultsSet{}, errors.New("interrupt: results channel unavailable")
	}
	var rs ToolResultsSet
	if err := c.resultsCh.Receive(ctx, &rs); err != nil {
		return ToolResultsSet{}, err
	}
	return rs, nil
}
