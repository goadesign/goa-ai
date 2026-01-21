// Package interrupt provides workflow signal handling for pausing and resuming
// agent runs. It exposes a Controller that workflows can use to react to
// external pause/resume requests via workflow engine signals.
package interrupt

import (
	"context"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	// PauseRequest carries metadata attached to a pause signal.
	PauseRequest = *api.PauseRequest

	// ResumeRequest carries metadata attached to a resume signal.
	ResumeRequest = *api.ResumeRequest

	// ClarificationAnswer carries a typed answer for a paused clarification request.
	ClarificationAnswer = *api.ClarificationAnswer

	// ConfirmationDecision carries a typed decision for a confirmation await.
	ConfirmationDecision = *api.ConfirmationDecision

	// ToolResultsSet carries results for an external tools await request.
	ToolResultsSet = *api.ToolResultsSet

	// Controller drains runtime interrupt signals and exposes helpers the
	// workflow loop can call to react to pause/resume and await requests.
	Controller struct {
		pauseCh   engine.Receiver[*api.PauseRequest]
		resumeCh  engine.Receiver[*api.ResumeRequest]
		clarifyCh engine.Receiver[*api.ClarificationAnswer]
		resultsCh engine.Receiver[*api.ToolResultsSet]
		confirmCh engine.Receiver[*api.ConfirmationDecision]
	}
)

const (
	// SignalPause is the workflow signal name used to pause a run.
	SignalPause = api.SignalPause
	// SignalResume is the workflow signal name used to resume a paused run.
	SignalResume = api.SignalResume

	// SignalProvideClarification delivers a ClarificationAnswer to a waiting run.
	SignalProvideClarification = api.SignalProvideClarification
	// SignalProvideToolResults delivers external tool results to a waiting run.
	SignalProvideToolResults = api.SignalProvideToolResults
	// SignalProvideConfirmation delivers a ConfirmationDecision to a waiting run.
	SignalProvideConfirmation = api.SignalProvideConfirmation
)

// NewController builds a controller wired to the workflow context signals.
func NewController(wfCtx engine.WorkflowContext) *Controller {
	return &Controller{
		pauseCh:   wfCtx.PauseRequests(),
		resumeCh:  wfCtx.ResumeRequests(),
		clarifyCh: wfCtx.ClarificationAnswers(),
		resultsCh: wfCtx.ExternalToolResults(),
		confirmCh: wfCtx.ConfirmationDecisions(),
	}
}

// PollPause attempts to dequeue a pause request without blocking.
func (c *Controller) PollPause() (PauseRequest, bool) {
	return c.pauseCh.ReceiveAsync()
}

// WaitResume blocks until a resume request is delivered or the timeout elapses.
//
// When timeout is <= 0, WaitResume blocks until a resume request is delivered or
// ctx is done.
func (c *Controller) WaitResume(ctx context.Context, timeout time.Duration) (ResumeRequest, error) {
	if timeout > 0 {
		return c.resumeCh.ReceiveWithTimeout(ctx, timeout)
	}
	return c.resumeCh.Receive(ctx)
}

// WaitProvideClarification blocks until a clarification answer is delivered or
// the timeout elapses.
//
// When timeout is <= 0, WaitProvideClarification blocks until an answer is
// delivered or ctx is done.
func (c *Controller) WaitProvideClarification(ctx context.Context, timeout time.Duration) (ClarificationAnswer, error) {
	if timeout > 0 {
		return c.clarifyCh.ReceiveWithTimeout(ctx, timeout)
	}
	return c.clarifyCh.Receive(ctx)
}

// WaitProvideToolResults blocks until external tool results are delivered or
// the timeout elapses.
//
// When timeout is <= 0, WaitProvideToolResults blocks until results are
// delivered or ctx is done.
func (c *Controller) WaitProvideToolResults(ctx context.Context, timeout time.Duration) (ToolResultsSet, error) {
	if timeout > 0 {
		return c.resultsCh.ReceiveWithTimeout(ctx, timeout)
	}
	return c.resultsCh.Receive(ctx)
}

// WaitProvideConfirmation blocks until a confirmation decision is delivered or
// the timeout elapses.
//
// When timeout is <= 0, WaitProvideConfirmation blocks until a decision is
// delivered or ctx is done.
func (c *Controller) WaitProvideConfirmation(ctx context.Context, timeout time.Duration) (ConfirmationDecision, error) {
	if timeout > 0 {
		return c.confirmCh.ReceiveWithTimeout(ctx, timeout)
	}
	return c.confirmCh.Receive(ctx)
}
